package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"blockwatch.cc/tzgo/tezos"
)

type TaqAccountBalance struct {
	Amount string `json:"amount"`
	Units  string `json:"units"`
}

type TaqAccount struct {
	Balance TaqAccountBalance `json:"balance"`
}

type TaqEnvironment struct {
	Type   string `json:"type"`
	Label  string `json:"label"`
	RpcUrl string `json:"rpcUrl,omitempty"`
}

type TaqPlugin struct {
	Type string `json:"type"`
	Name string `json:"name"`
}

type TaqConfig struct {
	Version            string                    `json:"version"`
	Language           string                    `json:"language"`
	ArtifactsDir       string                    `json:"artifactsDir"`
	ContractsDir       string                    `json:"contractsDir"`
	Accounts           map[string]TaqAccount     `json:"accounts,omitempty"`
	EnvironmentDefault string                    `json:"environmentDefault"`
	Environments       map[string]TaqEnvironment `json:"environments,omitempty"`
	Plugins            []TaqPlugin               `json:"plugins,omitempty"`
}

type TaqEnvAccount struct {
	EncryptedKey  string `json:"encryptedKey"`
	PublicKeyHash string `json:"publicKeyHash"`
	SecretKey     string `json:"secretKey"`
}

type TaqConfigEnv struct {
	Accounts       map[string]TaqEnvAccount `json:"accounts"`
	AccountDefault string                   `json:"accountDefault,omitempty"`
	RpcUrl         string                   `json:"rpcUrl"`
}

type TaqConfigSet struct {
	TaqConfig    TaqConfig
	TaqConfigEnv TaqConfigEnv
}

func ParseTaqConfig(taqConfigJSON string, taqEnvConfigJSON string) (*TaqConfigSet, error) {
	if taqConfigJSON == "" {
		return nil, fmt.Errorf("E_TAQ_ERROR: Missing taqueria project configuration")
	} else if taqEnvConfigJSON == "" {
		return nil, fmt.Errorf("E_TAQ_ERROR: Missing taqueria local environment configuration")
	}

	var config TaqConfig
	err := json.Unmarshal([]byte(taqConfigJSON), &config)
	if err != nil {
		return nil, fmt.Errorf("E_TAQ_ERROR: Invalid taqueria project configuration")
	}

	var envConfig TaqConfigEnv
	err = json.Unmarshal([]byte(taqEnvConfigJSON), &envConfig)
	if err != nil {
		return nil, fmt.Errorf("E_TAQ_ERROR: Invalid taqueria local environment configuration")
	}

	var configSet = TaqConfigSet{
		TaqConfig:    config,
		TaqConfigEnv: envConfig,
	}

	return &configSet, nil
}

func IsFlagSpecified(flag string) bool {
	// Check if os.Args includes "-{flag}"
	for _, arg := range os.Args {
		if strings.HasPrefix(arg, "-") && strings.TrimPrefix(arg, "-") == flag {
			return true
		}
	}
	return false
}

func getFlagValue(flag string) string {
	// Find which element has the value of "-{flag}" and return the next element
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "-") && strings.TrimPrefix(arg, "-") == flag {
			return os.Args[i+1]
		}
	}
	return ""
}

// Creates a new context, but reads the taqconfig and taqconfigenv flags to update the context with accounts and variables from the Taqueria project configuration
func NewTaqContext(ctx context.Context) (*Context, error) {
	taqConfigFlagSpecified := IsFlagSpecified("taqconfig")
	taqConfigEnvFlagSpecified := IsFlagSpecified("taqconfigenv")

	if taqConfigFlagSpecified != taqConfigEnvFlagSpecified {
		return nil, fmt.Errorf("E_TAQ_ERROR: taqconfig and taqconfigenv must be specified together")
	}

	var ectx Context = NewContext(ctx)
	taqConfigJSON := getFlagValue("taqconfig")
	taqConfigEnvJSON := getFlagValue("taqconfigenv")

	if taqConfigFlagSpecified && taqConfigEnvFlagSpecified {
		configSet, err := ParseTaqConfig(taqConfigJSON, taqConfigEnvJSON)
		if err != nil {
			return nil, err
		}
		err = UpdateTaqContext(&ectx, *configSet) // Removed & from ectx
		if err != nil {
			return nil, err
		}
	}

	return &ectx, nil // Removed & from ectx
}

func UpdateTaqContext(ctx *Context, taqConfigSet TaqConfigSet) error {

	if len(taqConfigSet.TaqConfig.Accounts) == 0 {
		return errors.New("E_TAQ_ERROR: No accounts configured")
	}

	if len(taqConfigSet.TaqConfigEnv.Accounts) == 0 {
		return errors.New("E_TAQ_ERROR: No accounts configured")
	}

	// If accountDefault hasn't been specified, try to use the first account available
	if taqConfigSet.TaqConfigEnv.AccountDefault == "" {
		if len(taqConfigSet.TaqConfigEnv.Accounts) > 0 {
			for accountName := range taqConfigSet.TaqConfigEnv.Accounts {
				taqConfigSet.TaqConfigEnv.AccountDefault = accountName
				break
			}
		} else {
			return errors.New("E_TAQ_ERROR: No default account specified")
		}
	}

	// Map the taqueria environment accounts to the context accounts
	mapTaqEnvAccountToContextAccount(ctx, taqConfigSet.TaqConfigEnv.Accounts, taqConfigSet.TaqConfigEnv.AccountDefault)

	return nil
}

func isDefaultAccount(accountName string, defaultAccountName string) bool {
	return accountName == defaultAccountName
}

func getAccountId(accountName string, defaultAccountName string, nextId int) int {
	if isDefaultAccount(accountName, defaultAccountName) {
		return -1
	}
	return nextId
}

func mapTaqEnvAccountToContextAccount(ctx *Context, taqAccounts map[string]TaqEnvAccount, taqAccountDefault string) {

	var count = 1
	for accountName, taqAccount := range taqAccounts {
		// Remove the prefix "encrypted:" from the secret key
		str_sk := strings.TrimPrefix(taqAccount.SecretKey, "unencrypted:")
		sk, err := tezos.ParsePrivateKey(str_sk)
		if err != nil {
			println(err)
		}

		account := Account{
			Address:    sk.Address(),
			PrivateKey: sk,
			Id:         getAccountId(accountName, taqAccountDefault, count),
		}

		if isDefaultAccount(accountName, taqAccountDefault) {
			ctx.BaseAccount = account
		}

		ctx.Accounts[account.Address] = account
		ctx.AddVariable(accountName, account.Address.String())

		count++
	}
}
