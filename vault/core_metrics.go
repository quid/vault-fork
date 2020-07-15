package vault

import (
	"context"
	"errors"
	"os"
	"strings"
	"time"

	"github.com/armon/go-metrics"
	"github.com/quid/vault/helper/metricsutil"
	"github.com/quid/vault/helper/namespace"
	"github.com/quid/vault/sdk/logical"
)

func (c *Core) metricsLoop(stopCh chan struct{}) {
	emitTimer := time.Tick(time.Second)
	writeTimer := time.Tick(c.counters.syncInterval)
	identityCountTimer := time.Tick(time.Minute * 10)

	// This loop covers
	// vault.expire.num_leases
	// vault.core.unsealed
	// vault.identity.num_entities
	// and the non-telemetry request counters shown in the UI.
	for {
		select {
		case <-emitTimer:
			c.metricsMutex.Lock()
			if c.expiration != nil {
				c.expiration.emitMetrics()
			}
			// Refresh the sealed gauge
			if c.Sealed() {
				c.metricSink.SetGaugeWithLabels([]string{"core", "unsealed"}, 0, nil)
			} else {
				c.metricSink.SetGaugeWithLabels([]string{"core", "unsealed"}, 1, nil)
			}
			c.metricsMutex.Unlock()

		case <-writeTimer:
			if stopped := grabLockOrStop(c.stateLock.RLock, c.stateLock.RUnlock, stopCh); stopped {
				// Go through the loop again, this time the stop channel case
				// should trigger
				continue
			}
			if c.perfStandby {
				syncCounter(c)
			} else {
				err := c.saveCurrentRequestCounters(context.Background(), time.Now())
				if err != nil {
					c.logger.Error("writing request counters to barrier", "err", err)
				}
			}
			c.stateLock.RUnlock()
		case <-identityCountTimer:
			// TODO: this can be replaced by the identity gauge counter; we need to
			// sum across all namespaces.
			go func() {
				ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
				defer cancel()
				entities, err := c.countActiveEntities(ctx)
				if err != nil {
					c.logger.Error("error counting identity entities", "err", err)
				} else {
					metrics.SetGauge([]string{"identity", "num_entities"}, float32(entities.Entities.Total))
				}
			}()

		case <-stopCh:
			return
		}
	}
}

// These wrappers are responsible for redirecting to the current instance of
// TokenStore; there is one per method because an additional level of abstraction
// seems confusing.
func (c *Core) tokenGaugeCollector(ctx context.Context) ([]metricsutil.GaugeLabelValues, error) {
	// stateLock or authLock protects the tokenStore pointer
	c.stateLock.RLock()
	ts := c.tokenStore
	c.stateLock.RUnlock()
	if ts == nil {
		return []metricsutil.GaugeLabelValues{}, errors.New("nil token store")
	}
	return ts.gaugeCollector(ctx)
}

func (c *Core) tokenGaugePolicyCollector(ctx context.Context) ([]metricsutil.GaugeLabelValues, error) {
	c.stateLock.RLock()
	ts := c.tokenStore
	c.stateLock.RUnlock()
	if ts == nil {
		return []metricsutil.GaugeLabelValues{}, errors.New("nil token store")
	}
	return ts.gaugeCollectorByPolicy(ctx)
}

func (c *Core) tokenGaugeMethodCollector(ctx context.Context) ([]metricsutil.GaugeLabelValues, error) {
	c.stateLock.RLock()
	ts := c.tokenStore
	c.stateLock.RUnlock()
	if ts == nil {
		return []metricsutil.GaugeLabelValues{}, errors.New("nil token store")
	}
	return ts.gaugeCollectorByMethod(ctx)
}

func (c *Core) tokenGaugeTtlCollector(ctx context.Context) ([]metricsutil.GaugeLabelValues, error) {
	c.stateLock.RLock()
	ts := c.tokenStore
	c.stateLock.RUnlock()
	if ts == nil {
		return []metricsutil.GaugeLabelValues{}, errors.New("nil token store")
	}
	return ts.gaugeCollectorByTtl(ctx)
}

// emitMetrics is used to start all the periodc metrics; all of them should
// be shut down when stopCh is closed.
func (c *Core) emitMetrics(stopCh chan struct{}) {
	// The gauge collection processes are started and stopped here
	// because there's more than one TokenManager created during startup,
	// but we only want one set of gauges.

	metricsInit := []struct {
		MetricName    []string
		MetadataLabel []metrics.Label
		CollectorFunc metricsutil.GaugeCollector
		DisableEnvVar string
	}{
		{
			[]string{"token", "count"},
			[]metrics.Label{{"gauge", "token_by_namespace"}},
			c.tokenGaugeCollector,
			"",
		},
		{
			[]string{"token", "count", "by_policy"},
			[]metrics.Label{{"gauge", "token_by_policy"}},
			c.tokenGaugePolicyCollector,
			"",
		},
		{
			[]string{"token", "count", "by_auth"},
			[]metrics.Label{{"gauge", "token_by_auth"}},
			c.tokenGaugeMethodCollector,
			"",
		},
		{
			[]string{"token", "count", "by_ttl"},
			[]metrics.Label{{"gauge", "token_by_ttl"}},
			c.tokenGaugeTtlCollector,
			"",
		},
		{
			[]string{"secret", "kv", "count"},
			[]metrics.Label{{"gauge", "kv_secrets_by_mountpoint"}},
			c.kvSecretGaugeCollector,
			"VAULT_DISABLE_KV_GAUGE",
		},
		{
			[]string{"identity", "entity", "count"},
			[]metrics.Label{{"gauge", "identity_by_namespace"}},
			c.entityGaugeCollector,
			"",
		},
		{
			[]string{"identity", "entity", "alias", "count"},
			[]metrics.Label{{"gauge", "identity_by_mountpoint"}},
			c.entityGaugeCollectorByMount,
			"",
		},
	}

	if c.MetricSink().GaugeInterval == time.Duration(0) {
		c.logger.Info("usage gauge collection is disabled")
	} else {
		for _, init := range metricsInit {
			if init.DisableEnvVar != "" {
				if os.Getenv(init.DisableEnvVar) != "" {
					c.logger.Info("usage gauge collection is disabled for",
						"metric", init.MetricName)
					continue
				}
			}

			proc, err := c.MetricSink().NewGaugeCollectionProcess(
				init.MetricName,
				init.MetadataLabel,
				init.CollectorFunc,
				c.logger,
			)
			if err != nil {
				c.logger.Error("failed to start collector", "metric", init.MetricName, "error", err)
			} else {
				go proc.Run()
				defer proc.Stop()
			}
		}
	}

	// When this returns, all the defers set up above will fire.
	c.metricsLoop(stopCh)
}

type kvMount struct {
	Namespace  *namespace.Namespace
	MountPoint string
	Version    string
	NumSecrets int
}

func (c *Core) findKvMounts() []*kvMount {
	mounts := make([]*kvMount, 0)

	c.mountsLock.RLock()
	defer c.mountsLock.RUnlock()

	for _, entry := range c.mounts.Entries {
		if entry.Type == "kv" {
			version, ok := entry.Options["version"]
			if !ok {
				version = "1"
			}
			mounts = append(mounts, &kvMount{
				Namespace:  entry.namespace,
				MountPoint: entry.Path,
				Version:    version,
				NumSecrets: 0,
			})
		}
	}
	return mounts
}

func (c *Core) kvCollectionErrorCount() {
	c.MetricSink().IncrCounterWithLabels(
		[]string{"metrics", "collection", "error"},
		1,
		[]metrics.Label{{"gauge", "kv_secrets_by_mountpoint"}},
	)
}

func (c *Core) walkKvMountSecrets(ctx context.Context, m *kvMount) {
	var subdirectories []string
	if m.Version == "1" {
		subdirectories = []string{m.Namespace.Path + m.MountPoint}
	} else {
		subdirectories = []string{m.Namespace.Path + m.MountPoint + "metadata/"}
	}

	for len(subdirectories) > 0 {
		// Check for cancellation
		select {
		case <-ctx.Done():
			return
		default:
			break
		}

		currentDirectory := subdirectories[0]
		subdirectories = subdirectories[1:]

		listRequest := &logical.Request{
			Operation: logical.ListOperation,
			Path:      currentDirectory,
		}
		resp, err := c.router.Route(ctx, listRequest)
		if err != nil {
			c.kvCollectionErrorCount()
			// ErrUnsupportedPath probably means that the mount is not there any more,
			// don't log those cases.
			if !strings.Contains(err.Error(), logical.ErrUnsupportedPath.Error()) {
				c.logger.Error("failed to perform internal KV list", "mount_point", m.MountPoint, "error", err)
				break
			}
			// Quit handling this mount point (but it'll still appear in the list)
			return
		}
		if resp == nil {
			continue
		}
		rawKeys, ok := resp.Data["keys"]
		if !ok {
			continue
		}
		keys, ok := rawKeys.([]string)
		if !ok {
			c.kvCollectionErrorCount()
			c.logger.Error("KV list keys are not a []string", "mount_point", m.MountPoint, "rawKeys", rawKeys)
			// Quit handling this mount point (but it'll still appear in the list)
			return
		}
		for _, path := range keys {
			if path[len(path)-1] == '/' {
				subdirectories = append(subdirectories, currentDirectory+path)
			} else {
				m.NumSecrets += 1
			}
		}
	}
}

func (c *Core) kvSecretGaugeCollector(ctx context.Context) ([]metricsutil.GaugeLabelValues, error) {
	// Find all KV mounts
	mounts := c.findKvMounts()
	results := make([]metricsutil.GaugeLabelValues, len(mounts))

	// Use a root namespace, so include namespace path
	// in any queries.
	ctx = namespace.RootContext(ctx)

	// Route list requests to all the identified mounts.
	// (All of these will show up as activity in the vault.route metric.)
	// Then we have to explore each subdirectory.
	for i, m := range mounts {
		// Check for cancellation, return empty array
		select {
		case <-ctx.Done():
			return []metricsutil.GaugeLabelValues{}, nil
		default:
			break
		}

		results[i].Labels = []metrics.Label{
			metricsutil.NamespaceLabel(m.Namespace),
			{"mount_point", m.MountPoint},
		}

		c.walkKvMountSecrets(ctx, m)
		results[i].Value = float32(m.NumSecrets)
	}

	return results, nil
}

func (c *Core) entityGaugeCollector(ctx context.Context) ([]metricsutil.GaugeLabelValues, error) {
	// Protect against concurrent changes during seal
	c.stateLock.RLock()
	identityStore := c.identityStore
	c.stateLock.RUnlock()
	if identityStore == nil {
		return []metricsutil.GaugeLabelValues{}, errors.New("nil identity store")
	}

	byNamespace, err := identityStore.countEntitiesByNamespace(ctx)
	if err != nil {
		return []metricsutil.GaugeLabelValues{}, err
	}

	// No check for expiration here; the bulk of the work should be in
	// counting the entities.
	allNamespaces := c.collectNamespaces()
	values := make([]metricsutil.GaugeLabelValues, len(allNamespaces))
	for i := range values {
		values[i].Labels = []metrics.Label{
			metricsutil.NamespaceLabel(allNamespaces[i]),
		}
		values[i].Value = float32(byNamespace[allNamespaces[i].ID])
	}

	return values, nil
}

func (c *Core) entityGaugeCollectorByMount(ctx context.Context) ([]metricsutil.GaugeLabelValues, error) {
	c.stateLock.RLock()
	identityStore := c.identityStore
	c.stateLock.RUnlock()
	if identityStore == nil {
		return []metricsutil.GaugeLabelValues{}, errors.New("nil identity store")
	}

	byAccessor, err := identityStore.countEntitiesByMountAccessor(ctx)
	if err != nil {
		return []metricsutil.GaugeLabelValues{}, err
	}

	values := make([]metricsutil.GaugeLabelValues, 0)
	for accessor, count := range byAccessor {
		// Terminate if taking too long to do the translation
		select {
		case <-ctx.Done():
			return values, errors.New("context cancelled")
		default:
			break
		}

		c.stateLock.RLock()
		mountEntry := c.router.MatchingMountByAccessor(accessor)
		c.stateLock.RUnlock()
		if mountEntry == nil {
			continue
		}
		values = append(values, metricsutil.GaugeLabelValues{
			Labels: []metrics.Label{
				metricsutil.NamespaceLabel(mountEntry.namespace),
				{"auth_method", mountEntry.Type},
				{"mount_point", "auth/" + mountEntry.Path},
			},
			Value: float32(count),
		})
	}

	return values, nil
}
