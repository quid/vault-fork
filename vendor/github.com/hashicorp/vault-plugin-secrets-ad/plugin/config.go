package plugin

import (
	"fmt"
	"strings"

	"github.com/quid/vault-plugin-secrets-ad/plugin/client"
)

type configuration struct {
	PasswordConf          passwordConf
	ADConf                *client.ADConf
	LastRotationTolerance int
}

type passwordConf struct {
	TTL    int `json:"ttl"`
	MaxTTL int `json:"max_ttl"`

	// Mutually exclusive with Length and Formatter
	PolicyName string `json:"password_policy"`

	// Length of the password to generate. Mutually exclusive with PolicyName.
	// Deprecated
	Length int `json:"length"`

	// Formatter describes how to format a password. This allows for prefixes and suffixes on the password.
	// Mutually exclusive with PolicyName.
	// Deprecated
	Formatter string `json:"formatter"`
}

func (c passwordConf) Map() map[string]interface{} {
	return map[string]interface{}{
		"ttl":         c.TTL,
		"max_ttl":     c.MaxTTL,
		"length":      c.Length,
		"formatter":   c.Formatter,
		"policy_name": c.PolicyName,
	}
}

// validate returns an error if the configuration is invalid/unable to process for whatever reason.
func (c passwordConf) validate() error {
	if c.PolicyName != "" &&
		(c.Length != 0 || c.Formatter != "") {
		return fmt.Errorf("cannot set password_policy and either length or formatter")
	}

	// Don't validate the length and formatter fields if a policy is set
	if c.PolicyName != "" {
		return nil
	}

	// Check for if there's no formatter.
	if c.Formatter == "" {
		if c.Length < len(passwordComplexityPrefix)+minimumLengthOfComplexString {
			return fmt.Errorf("it's not possible to generate a _secure_ password of length %d, please boost length to %d, though Vault recommends higher",
				c.Length, minimumLengthOfComplexString+len(passwordComplexityPrefix))
		}
		return nil
	}

	// Check for if there is a formatter.
	if lengthOfPassword(c.Formatter, c.Length) < minimumLengthOfComplexString {
		return fmt.Errorf("since the desired length is %d, it isn't possible to generate a sufficiently complex password - please increase desired length or remove characters from the formatter", c.Length)
	}
	numPwdFields := strings.Count(c.Formatter, pwdFieldTmpl)
	if numPwdFields == 0 {
		return fmt.Errorf("%s must contain password replacement field of %s", c.Formatter, pwdFieldTmpl)
	}
	if numPwdFields > 1 {
		return fmt.Errorf("%s must contain ONE password replacement field of %s", c.Formatter, pwdFieldTmpl)
	}
	return nil
}
