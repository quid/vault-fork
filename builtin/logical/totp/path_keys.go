package totp

import (
	"bytes"
	"context"
	"encoding/base32"
	"encoding/base64"
	"fmt"
	"image/png"
	"net/url"
	"strconv"
	"strings"

	"github.com/hashicorp/errwrap"
	"github.com/quid/vault/sdk/framework"
	"github.com/quid/vault/sdk/logical"
	otplib "github.com/pquerna/otp"
	totplib "github.com/pquerna/otp/totp"
)

func pathListKeys(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "keys/?$",

		Callbacks: map[logical.Operation]framework.OperationFunc{
			logical.ListOperation: b.pathKeyList,
		},

		HelpSynopsis:    pathKeyHelpSyn,
		HelpDescription: pathKeyHelpDesc,
	}
}

func pathKeys(b *backend) *framework.Path {
	return &framework.Path{
		Pattern: "keys/" + framework.GenericNameWithAtRegex("name"),
		Fields: map[string]*framework.FieldSchema{
			"name": {
				Type:        framework.TypeString,
				Description: "Name of the key.",
			},

			"generate": {
				Type:        framework.TypeBool,
				Default:     false,
				Description: "Determines if a key should be generated by Vault or if a key is being passed from another service.",
			},

			"exported": {
				Type:        framework.TypeBool,
				Default:     true,
				Description: "Determines if a QR code and url are returned upon generating a key. Only used if generate is true.",
			},

			"key_size": {
				Type:        framework.TypeInt,
				Default:     20,
				Description: "Determines the size in bytes of the generated key. Only used if generate is true.",
			},

			"key": {
				Type:        framework.TypeString,
				Description: "The shared master key used to generate a TOTP token. Only used if generate is false.",
			},

			"issuer": {
				Type:        framework.TypeString,
				Description: `The name of the key's issuing organization. Required if generate is true.`,
			},

			"account_name": {
				Type:        framework.TypeString,
				Description: `The name of the account associated with the key. Required if generate is true.`,
			},

			"period": {
				Type:        framework.TypeDurationSecond,
				Default:     30,
				Description: `The length of time used to generate a counter for the TOTP token calculation.`,
			},

			"algorithm": {
				Type:        framework.TypeString,
				Default:     "SHA1",
				Description: `The hashing algorithm used to generate the TOTP token. Options include SHA1, SHA256 and SHA512.`,
			},

			"digits": {
				Type:        framework.TypeInt,
				Default:     6,
				Description: `The number of digits in the generated TOTP token. This value can either be 6 or 8.`,
			},

			"skew": {
				Type:        framework.TypeInt,
				Default:     1,
				Description: `The number of delay periods that are allowed when validating a TOTP token. This value can either be 0 or 1. Only used if generate is true.`,
			},

			"qr_size": {
				Type:        framework.TypeInt,
				Default:     200,
				Description: `The pixel size of the generated square QR code. Only used if generate is true and exported is true. If this value is 0, a QR code will not be returned.`,
			},

			"url": {
				Type:        framework.TypeString,
				Description: `A TOTP url string containing all of the parameters for key setup. Only used if generate is false.`,
			},
		},

		Callbacks: map[logical.Operation]framework.OperationFunc{
			logical.ReadOperation:   b.pathKeyRead,
			logical.UpdateOperation: b.pathKeyCreate,
			logical.DeleteOperation: b.pathKeyDelete,
		},

		HelpSynopsis:    pathKeyHelpSyn,
		HelpDescription: pathKeyHelpDesc,
	}
}

func (b *backend) Key(ctx context.Context, s logical.Storage, n string) (*keyEntry, error) {
	entry, err := s.Get(ctx, "key/"+n)
	if err != nil {
		return nil, err
	}
	if entry == nil {
		return nil, nil
	}

	var result keyEntry
	if err := entry.DecodeJSON(&result); err != nil {
		return nil, err
	}

	return &result, nil
}

func (b *backend) pathKeyDelete(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	err := req.Storage.Delete(ctx, "key/"+data.Get("name").(string))
	if err != nil {
		return nil, err
	}

	return nil, nil
}

func (b *backend) pathKeyRead(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	key, err := b.Key(ctx, req.Storage, data.Get("name").(string))
	if err != nil {
		return nil, err
	}
	if key == nil {
		return nil, nil
	}

	// Translate algorithm back to string
	algorithm := key.Algorithm.String()

	// Return values of key
	return &logical.Response{
		Data: map[string]interface{}{
			"issuer":       key.Issuer,
			"account_name": key.AccountName,
			"period":       key.Period,
			"algorithm":    algorithm,
			"digits":       key.Digits,
		},
	}, nil
}

func (b *backend) pathKeyList(ctx context.Context, req *logical.Request, d *framework.FieldData) (*logical.Response, error) {
	entries, err := req.Storage.List(ctx, "key/")
	if err != nil {
		return nil, err
	}

	return logical.ListResponse(entries), nil
}

func (b *backend) pathKeyCreate(ctx context.Context, req *logical.Request, data *framework.FieldData) (*logical.Response, error) {
	name := data.Get("name").(string)
	generate := data.Get("generate").(bool)
	exported := data.Get("exported").(bool)
	keyString := data.Get("key").(string)
	issuer := data.Get("issuer").(string)
	accountName := data.Get("account_name").(string)
	period := data.Get("period").(int)
	algorithm := data.Get("algorithm").(string)
	digits := data.Get("digits").(int)
	skew := data.Get("skew").(int)
	qrSize := data.Get("qr_size").(int)
	keySize := data.Get("key_size").(int)
	inputURL := data.Get("url").(string)

	if generate {
		if keyString != "" {
			return logical.ErrorResponse("a key should not be passed if generate is true"), nil
		}
		if inputURL != "" {
			return logical.ErrorResponse("a url should not be passed if generate is true"), nil
		}
	}

	// Read parameters from url if given
	if inputURL != "" {
		//Parse url
		urlObject, err := url.Parse(inputURL)
		if err != nil {
			return logical.ErrorResponse("an error occurred while parsing url string"), err
		}

		//Set up query object
		urlQuery := urlObject.Query()
		path := strings.TrimPrefix(urlObject.Path, "/")
		index := strings.Index(path, ":")

		//Read issuer
		urlIssuer := urlQuery.Get("issuer")
		if urlIssuer != "" {
			issuer = urlIssuer
		} else {
			if index != -1 {
				issuer = path[:index]
			}
		}

		//Read account name
		if index == -1 {
			accountName = path
		} else {
			accountName = path[index+1:]
		}

		//Read key string
		keyString = urlQuery.Get("secret")

		//Read period
		periodQuery := urlQuery.Get("period")
		if periodQuery != "" {
			periodInt, err := strconv.Atoi(periodQuery)
			if err != nil {
				return logical.ErrorResponse("an error occurred while parsing period value in url"), err
			}
			period = periodInt
		}

		//Read digits
		digitsQuery := urlQuery.Get("digits")
		if digitsQuery != "" {
			digitsInt, err := strconv.Atoi(digitsQuery)
			if err != nil {
				return logical.ErrorResponse("an error occurred while parsing digits value in url"), err
			}
			digits = digitsInt
		}

		//Read algorithm
		algorithmQuery := urlQuery.Get("algorithm")
		if algorithmQuery != "" {
			algorithm = algorithmQuery
		}
	}

	// Translate digits and algorithm to a format the totp library understands
	var keyDigits otplib.Digits
	switch digits {
	case 6:
		keyDigits = otplib.DigitsSix
	case 8:
		keyDigits = otplib.DigitsEight
	default:
		return logical.ErrorResponse("the digits value can only be 6 or 8"), nil
	}

	var keyAlgorithm otplib.Algorithm
	switch algorithm {
	case "SHA1":
		keyAlgorithm = otplib.AlgorithmSHA1
	case "SHA256":
		keyAlgorithm = otplib.AlgorithmSHA256
	case "SHA512":
		keyAlgorithm = otplib.AlgorithmSHA512
	default:
		return logical.ErrorResponse("the algorithm value is not valid"), nil
	}

	// Enforce input value requirements
	if period <= 0 {
		return logical.ErrorResponse("the period value must be greater than zero"), nil
	}

	switch skew {
	case 0:
	case 1:
	default:
		return logical.ErrorResponse("the skew value must be 0 or 1"), nil
	}

	// QR size can be zero but it shouldn't be negative
	if qrSize < 0 {
		return logical.ErrorResponse("the qr_size value must be greater than or equal to zero"), nil
	}

	if keySize <= 0 {
		return logical.ErrorResponse("the key_size value must be greater than zero"), nil
	}

	// Period, Skew and Key Size need to be unsigned ints
	uintPeriod := uint(period)
	uintSkew := uint(skew)
	uintKeySize := uint(keySize)

	var response *logical.Response

	switch generate {
	case true:
		// If the key is generated, Account Name and Issuer are required.
		if accountName == "" {
			return logical.ErrorResponse("the account_name value is required for generated keys"), nil
		}

		if issuer == "" {
			return logical.ErrorResponse("the issuer value is required for generated keys"), nil
		}

		// Generate a new key
		keyObject, err := totplib.Generate(totplib.GenerateOpts{
			Issuer:      issuer,
			AccountName: accountName,
			Period:      uintPeriod,
			Digits:      keyDigits,
			Algorithm:   keyAlgorithm,
			SecretSize:  uintKeySize,
			Rand:        b.GetRandomReader(),
		})
		if err != nil {
			return logical.ErrorResponse("an error occurred while generating a key"), err
		}

		// Get key string value
		keyString = keyObject.Secret()

		// Skip returning the QR code and url if exported is set to false
		if exported {
			// Prepare the url and barcode
			urlString := keyObject.String()

			// Don't include QR code if size is set to zero
			if qrSize == 0 {
				response = &logical.Response{
					Data: map[string]interface{}{
						"url": urlString,
					},
				}
			} else {
				barcode, err := keyObject.Image(qrSize, qrSize)
				if err != nil {
					return nil, errwrap.Wrapf("failed to generate QR code image: {{err}}", err)
				}

				var buff bytes.Buffer
				png.Encode(&buff, barcode)
				b64Barcode := base64.StdEncoding.EncodeToString(buff.Bytes())
				response = &logical.Response{
					Data: map[string]interface{}{
						"url":     urlString,
						"barcode": b64Barcode,
					},
				}
			}
		}
	default:
		if keyString == "" {
			return logical.ErrorResponse("the key value is required"), nil
		}

		_, err := base32.StdEncoding.DecodeString(strings.ToUpper(keyString))
		if err != nil {
			return logical.ErrorResponse(fmt.Sprintf(
				"invalid key value: %s", err)), nil
		}
	}

	// Store it
	entry, err := logical.StorageEntryJSON("key/"+name, &keyEntry{
		Key:         keyString,
		Issuer:      issuer,
		AccountName: accountName,
		Period:      uintPeriod,
		Algorithm:   keyAlgorithm,
		Digits:      keyDigits,
		Skew:        uintSkew,
	})
	if err != nil {
		return nil, err
	}
	if err := req.Storage.Put(ctx, entry); err != nil {
		return nil, err
	}

	return response, nil
}

type keyEntry struct {
	Key         string           `json:"key" mapstructure:"key" structs:"key"`
	Issuer      string           `json:"issuer" mapstructure:"issuer" structs:"issuer"`
	AccountName string           `json:"account_name" mapstructure:"account_name" structs:"account_name"`
	Period      uint             `json:"period" mapstructure:"period" structs:"period"`
	Algorithm   otplib.Algorithm `json:"algorithm" mapstructure:"algorithm" structs:"algorithm"`
	Digits      otplib.Digits    `json:"digits" mapstructure:"digits" structs:"digits"`
	Skew        uint             `json:"skew" mapstructure:"skew" structs:"skew"`
}

const pathKeyHelpSyn = `
Manage the keys that can be created with this backend.
`

const pathKeyHelpDesc = `
This path lets you manage the keys that can be created with this backend.

`
