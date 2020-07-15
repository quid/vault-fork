// Copyright © 2019, Oracle and/or its affiliates.
package oci

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/ioutil"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/armon/go-metrics"
	"github.com/hashicorp/errwrap"
	"github.com/hashicorp/go-uuid"
	"github.com/quid/vault/sdk/physical"
	"github.com/oracle/oci-go-sdk/objectstorage"
)

// The lock implementation below prioritizes ensuring that there are not 2 primary at any given point in time
// over high availability of the primary instance

// Verify Backend satisfies the correct interfaces
var _ physical.HABackend = (*Backend)(nil)
var _ physical.Lock = (*Lock)(nil)

const (
	// LockRenewInterval is the time to wait between lock renewals.
	LockRenewInterval = 3 * time.Second

	// LockRetryInterval is the amount of time to wait if the lock fails before trying again.
	LockRetryInterval = 5 * time.Second

	// LockWatchRetryInterval is the amount of time to wait if a watch fails before trying again.
	LockWatchRetryInterval = 2 * time.Second

	// LockTTL is the default lock TTL.
	LockTTL = 15 * time.Second

	// LockWatchRetryMax is the number of times to retry a failed watch before signaling that leadership is lost.
	LockWatchRetryMax = 4

	// LockCacheMinAcceptableAge is minimum cache age in seconds to determine that its safe for a secondary instance
	// to acquire lock.
	LockCacheMinAcceptableAge = 45 * time.Second

	// LockWriteRetriesOnFailures is the number of retries that are made on write 5xx failures.
	LockWriteRetriesOnFailures = 4

	ObjectStorageCallsReadTimeout = 3 * time.Second

	ObjectStorageCallsWriteTimeout = 3 * time.Second
)

type LockCache struct {
	// ETag values are unique identifiers generated by the OCI service and changed every time the object is modified.
	etag       string
	lastUpdate time.Time
	lockRecord *LockRecord
}

type Lock struct {
	// backend is the underlying physical backend.
	backend *Backend

	// Key is the name of the Key. Value is the Value of the Key.
	key, value string

	// held is a boolean indicating if the lock is currently held.
	held bool

	// Identity is the internal Identity of this Key (unique to this server instance).
	identity string

	internalLock sync.Mutex

	// stopCh is the channel that stops all operations. It may be closed in the
	// event of a leader loss or graceful shutdown. stopped is a boolean
	// indicating if we are stopped - it exists to prevent double closing the
	// channel. stopLock is a mutex around the locks.
	stopCh   chan struct{}
	stopped  bool
	stopLock sync.Mutex

	lockRecordCache atomic.Value

	// Allow modifying the Lock durations for ease of unit testing.
	renewInterval      time.Duration
	retryInterval      time.Duration
	ttl                time.Duration
	watchRetryInterval time.Duration
	watchRetryMax      int
}

type LockRecord struct {
	Key      string
	Value    string
	Identity string
}

var (
	metricLockUnlock  = []string{"oci", "lock", "unlock"}
	metricLockLock    = []string{"oci", "lock", "lock"}
	metricLockValue   = []string{"oci", "lock", "Value"}
	metricLeaderValue = []string{"oci", "leader", "Value"}
)

func (b *Backend) HAEnabled() bool {
	return b.haEnabled
}

// LockWith acquires a mutual exclusion based on the given Key.
func (b *Backend) LockWith(key, value string) (physical.Lock, error) {
	identity, err := uuid.GenerateUUID()
	if err != nil {
		return nil, errwrap.Wrapf("Lock with: {{err}}", err)
	}
	return &Lock{
		backend:  b,
		key:      key,
		value:    value,
		identity: identity,
		stopped:  true,

		renewInterval:      LockRenewInterval,
		retryInterval:      LockRetryInterval,
		ttl:                LockTTL,
		watchRetryInterval: LockWatchRetryInterval,
		watchRetryMax:      LockWatchRetryMax,
	}, nil
}

func (l *Lock) Lock(stopCh <-chan struct{}) (<-chan struct{}, error) {
	l.backend.logger.Debug("Lock() called")
	defer metrics.MeasureSince(metricLockLock, time.Now().UTC())
	l.internalLock.Lock()
	defer l.internalLock.Unlock()
	if l.held {
		return nil, errors.New("lock already held")
	}

	// Attempt to lock - this function blocks until a lock is acquired or an error
	// occurs.
	acquired, err := l.attemptLock(stopCh)
	if err != nil {
		return nil, errwrap.Wrapf("lock: {{err}}", err)
	}
	if !acquired {
		return nil, nil
	}

	// We have the lock now
	l.held = true

	// Build the locks
	l.stopLock.Lock()
	l.stopCh = make(chan struct{})
	l.stopped = false
	l.stopLock.Unlock()

	// Periodically renew and watch the lock
	go l.renewLock()
	go l.watchLock()

	return l.stopCh, nil
}

// attemptLock attempts to acquire a lock. If the given channel is closed, the
// acquisition attempt stops. This function returns when a lock is acquired or
// an error occurs.
func (l *Lock) attemptLock(stopCh <-chan struct{}) (bool, error) {
	l.backend.logger.Debug("AttemptLock() called")
	ticker := time.NewTicker(l.retryInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			acquired, err := l.writeLock()
			if err != nil {
				return false, errwrap.Wrapf("attempt lock: {{err}}", err)
			}
			if !acquired {
				continue
			}

			return true, nil
		case <-stopCh:
			return false, nil
		}
	}
}

// renewLock renews the given lock until the channel is closed.
func (l *Lock) renewLock() {
	l.backend.logger.Debug("RenewLock() called")
	ticker := time.NewTicker(l.renewInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			l.writeLock()
		case <-l.stopCh:
			return
		}
	}
}

func loadLockRecordCache(l *Lock) *LockCache {
	lockRecordCache := l.lockRecordCache.Load()
	if lockRecordCache == nil {
		return nil
	}
	return lockRecordCache.(*LockCache)
}

// watchLock checks whether the lock has changed in the table and closes the
// leader channel accordingly. If an error occurs during the check, watchLock
// will retry the operation and then close the leader channel if it can't
// succeed after retries.
func (l *Lock) watchLock() {
	l.backend.logger.Debug("WatchLock() called")
	retries := 0
	ticker := time.NewTicker(l.watchRetryInterval)
	defer ticker.Stop()

OUTER:
	for {
		// Check if the channel is already closed
		select {
		case <-l.stopCh:
			l.backend.logger.Debug("WatchLock():Stop lock signaled/closed.")
			break OUTER
		default:
		}

		// Check if we've exceeded retries
		if retries >= l.watchRetryMax-1 {
			l.backend.logger.Debug("WatchLock: Failed to get lock data from object storage. Giving up the lease after max retries")
			break OUTER
		}

		// Wait for the timer
		select {
		case <-ticker.C:
		case <-l.stopCh:
			break OUTER
		}

		lockRecordCache := loadLockRecordCache(l)
		if (lockRecordCache == nil) ||
			(lockRecordCache.lockRecord == nil) ||
			(lockRecordCache.lockRecord.Identity != l.identity) ||
			(time.Now().Sub(lockRecordCache.lastUpdate) > l.ttl) {
			l.backend.logger.Debug("WatchLock: Lock record cache is nil, stale or does not belong to self.")
			break OUTER
		}

		lockRecord, _, err := l.get(context.Background())
		if err != nil {
			retries++
			l.backend.logger.Debug("WatchLock: Failed to get lock data from object storage. Retrying..")
			metrics.SetGauge(metricHaWatchLockRetriable, 1)
			continue
		}

		if (lockRecord == nil) || (lockRecord.Identity != l.identity) {
			l.backend.logger.Debug("WatchLock: Lock record cache is nil or does not belong to self.")
			break OUTER
		}

		// reset retries counter on success
		retries = 0
		l.backend.logger.Debug("WatchLock() successful")
		metrics.SetGauge(metricHaWatchLockRetriable, 0)
	}

	l.stopLock.Lock()
	defer l.stopLock.Unlock()
	if !l.stopped {
		l.stopped = true
		l.backend.logger.Debug("Closing the stop channel to give up leadership.")
		close(l.stopCh)
	}
}

func (l *Lock) Unlock() error {
	l.backend.logger.Debug("Unlock() called")
	defer metrics.MeasureSince(metricLockUnlock, time.Now().UTC())

	l.internalLock.Lock()
	defer l.internalLock.Unlock()
	if !l.held {
		return nil
	}

	// Stop any existing locking or renewal attempts
	l.stopLock.Lock()
	if !l.stopped {
		l.stopped = true
		close(l.stopCh)
	}
	l.stopLock.Unlock()

	// We are no longer holding the lock
	l.held = false

	// Get current lock record
	currentLockRecord, etag, err := l.get(context.Background())
	if err != nil {
		return errwrap.Wrapf("error reading lock record: {{err}}", err)
	}

	if currentLockRecord != nil && currentLockRecord.Identity == l.identity {

		defer metrics.MeasureSince(metricDeleteHa, time.Now())
		opcClientRequestId, err := uuid.GenerateUUID()
		if err != nil {
			l.backend.logger.Debug("Unlock: error generating UUID")
			return errwrap.Wrapf("failed to generate UUID: {{err}}", err)
		}
		l.backend.logger.Debug("Unlock", "opc-client-request-id", opcClientRequestId)
		request := objectstorage.DeleteObjectRequest{
			NamespaceName:      &l.backend.namespaceName,
			BucketName:         &l.backend.lockBucketName,
			ObjectName:         &l.key,
			IfMatch:            &etag,
			OpcClientRequestId: &opcClientRequestId,
		}

		response, err := l.backend.client.DeleteObject(context.Background(), request)
		l.backend.logRequest("deleteHA", response.RawResponse, response.OpcClientRequestId, response.OpcRequestId, err)

		if err != nil {
			metrics.IncrCounter(metricDeleteFailed, 1)
			return errwrap.Wrapf("write lock: {{err}}", err)
		}
	}

	return nil
}

func (l *Lock) Value() (bool, string, error) {
	l.backend.logger.Debug("Value() called")
	defer metrics.MeasureSince(metricLockValue, time.Now().UTC())

	lockRecord, _, err := l.get(context.Background())
	if err != nil {
		return false, "", err
	}
	if lockRecord == nil {
		return false, "", err
	}
	return true, lockRecord.Value, nil
}

// get retrieves the Value for the lock.
func (l *Lock) get(ctx context.Context) (*LockRecord, string, error) {
	l.backend.logger.Debug("Called getLockRecord()")

	// Read lock Key

	defer metrics.MeasureSince(metricGetHa, time.Now())
	opcClientRequestId, err := uuid.GenerateUUID()
	if err != nil {
		l.backend.logger.Error("getHa: error generating UUID")
		return nil, "", errwrap.Wrapf("failed to generate UUID: {{err}}", err)
	}
	l.backend.logger.Debug("getHa", "opc-client-request-id", opcClientRequestId)

	request := objectstorage.GetObjectRequest{
		NamespaceName:      &l.backend.namespaceName,
		BucketName:         &l.backend.lockBucketName,
		ObjectName:         &l.key,
		OpcClientRequestId: &opcClientRequestId,
	}

	ctx, cancel := context.WithTimeout(ctx, ObjectStorageCallsReadTimeout)
	defer cancel()

	response, err := l.backend.client.GetObject(ctx, request)
	l.backend.logRequest("getHA", response.RawResponse, response.OpcClientRequestId, response.OpcRequestId, err)

	if err != nil {
		if response.RawResponse != nil && response.RawResponse.StatusCode == http.StatusNotFound {
			return nil, "", nil
		}

		metrics.IncrCounter(metricGetFailed, 1)
		l.backend.logger.Error("Error calling GET", "err", err)
		return nil, "", errwrap.Wrapf(fmt.Sprintf("failed to read Value for %q: {{err}}", l.key), err)
	}

	defer response.RawResponse.Body.Close()

	body, err := ioutil.ReadAll(response.Content)
	if err != nil {
		metrics.IncrCounter(metricGetFailed, 1)
		l.backend.logger.Error("Error reading content", "err", err)
		return nil, "", errwrap.Wrapf("failed to decode Value into bytes: {{err}}", err)
	}

	var lockRecord LockRecord
	err = json.Unmarshal(body, &lockRecord)
	if err != nil {
		metrics.IncrCounter(metricGetFailed, 1)
		l.backend.logger.Error("Error un-marshalling content", "err", err)
		return nil, "", errwrap.Wrapf(fmt.Sprintf("failed to read Value for %q: {{err}}", l.key), err)
	}

	return &lockRecord, *response.ETag, nil
}

func (l *Lock) writeLock() (bool, error) {
	l.backend.logger.Debug("WriteLock() called")

	// Create a transaction to read and the update (maybe)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// The transaction will be retried, and it could sit in a queue behind, say,
	// the delete operation. To stop the transaction, we close the context when
	// the associated stopCh is received.
	go func() {
		select {
		case <-l.stopCh:
			cancel()
		case <-ctx.Done():
		}
	}()

	lockRecordCache := loadLockRecordCache(l)
	if (lockRecordCache == nil) || lockRecordCache.lockRecord == nil ||
		lockRecordCache.lockRecord.Identity != l.identity ||
		time.Now().Sub(lockRecordCache.lastUpdate) > l.ttl {
		// case secondary
		currentLockRecord, currentEtag, err := l.get(ctx)
		if err != nil {
			return false, errwrap.Wrapf("error reading lock record: {{err}}", err)
		}

		if (lockRecordCache == nil) || lockRecordCache.etag != currentEtag {
			// update cached lock record
			l.lockRecordCache.Store(&LockCache{
				etag:       currentEtag,
				lastUpdate: time.Now().UTC(),
				lockRecord: currentLockRecord,
			})

			lockRecordCache = loadLockRecordCache(l)
		}

		// Current lock record being null implies that there is no leader. In this case we want to try acquiring lock.
		if currentLockRecord != nil && time.Now().Sub(lockRecordCache.lastUpdate) < LockCacheMinAcceptableAge {
			return false, nil
		}
		// cache is old enough and current, try acquiring lock as secondary
	}

	newLockRecord := &LockRecord{
		Key:      l.key,
		Value:    l.value,
		Identity: l.identity,
	}

	newLockRecordJson, err := json.Marshal(newLockRecord)
	if err != nil {
		return false, errwrap.Wrapf("error reading lock record: {{err}}", err)
	}

	defer metrics.MeasureSince(metricPutHa, time.Now())

	opcClientRequestId, err := uuid.GenerateUUID()
	if err != nil {
		l.backend.logger.Error("putHa: error generating UUID")
		return false, errwrap.Wrapf("failed to generate UUID", err)
	}
	l.backend.logger.Debug("putHa", "opc-client-request-id", opcClientRequestId)
	size := int64(len(newLockRecordJson))
	putRequest := objectstorage.PutObjectRequest{
		NamespaceName:      &l.backend.namespaceName,
		BucketName:         &l.backend.lockBucketName,
		ObjectName:         &l.key,
		ContentLength:      &size,
		PutObjectBody:      ioutil.NopCloser(bytes.NewReader(newLockRecordJson)),
		OpcMeta:            nil,
		OpcClientRequestId: &opcClientRequestId,
	}

	if lockRecordCache.etag == "" {
		noneMatch := "*"
		putRequest.IfNoneMatch = &noneMatch
	} else {
		putRequest.IfMatch = &lockRecordCache.etag
	}

	newtEtag := ""
	for i := 1; i <= LockWriteRetriesOnFailures; i++ {
		writeCtx, writeCancel := context.WithTimeout(ctx, ObjectStorageCallsWriteTimeout)
		defer writeCancel()

		putObjectResponse, putObjectError := l.backend.client.PutObject(writeCtx, putRequest)
		l.backend.logRequest("putHA", putObjectResponse.RawResponse, putObjectResponse.OpcClientRequestId, putObjectResponse.OpcRequestId, putObjectError)

		if putObjectError == nil {
			newtEtag = *putObjectResponse.ETag
			putObjectResponse.RawResponse.Body.Close()
			break
		}

		err = putObjectError

		if putObjectResponse.RawResponse == nil {
			metrics.IncrCounter(metricPutFailed, 1)
			l.backend.logger.Error("PUT", "err", err)
			break
		}

		putObjectResponse.RawResponse.Body.Close()

		// Retry if the return code is 5xx
		if (putObjectResponse.RawResponse.StatusCode / 100) == 5 {
			metrics.IncrCounter(metricPutFailed, 1)
			l.backend.logger.Warn("PUT. Retrying..", "err", err)
			time.Sleep(time.Duration(100*i) * time.Millisecond)
		} else {
			l.backend.logger.Error("PUT", "err", err)
			break
		}
	}

	if err != nil {
		return false, errwrap.Wrapf("write lock: {{err}}", err)
	}

	l.backend.logger.Debug("Lock written", string(newLockRecordJson))

	l.lockRecordCache.Store(&LockCache{
		etag:       newtEtag,
		lastUpdate: time.Now().UTC(),
		lockRecord: newLockRecord,
	})

	metrics.SetGauge(metricLeaderValue, 1)
	return true, nil
}
