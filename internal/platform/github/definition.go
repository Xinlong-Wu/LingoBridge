package github

import (
	"fmt"
	"os"

	"lingobridge/internal/commands"
	"lingobridge/internal/core"
	"lingobridge/internal/platform"
	"lingobridge/internal/store"
)

const accountNewUsage = "lingobridge account new github [--name <name>] [--app-id <id>] [--installation-id <id>] [--private-key-path <pem>] --repo owner/repo [--repo owner/other] [--poll-interval 2m] [--base-url <url>] [--web-url <url>]"

// Definition returns the built-in GitHub platform registration.
func Definition() platform.Definition {
	return platform.Definition{
		ID:              store.PlatformGitHub,
		AccountNewUsage: accountNewUsage,
		ParseAccountNewFlags: func(args []string, io platform.AccountNewIO) (platform.AccountNewOptions, error) {
			accountIO := normalizeAccountNewIO(io)
			values, err := ParseAccountNewFlags(args, accountIO.In, accountIO.Out)
			if err != nil {
				return platform.AccountNewOptions{}, err
			}
			return platform.AccountNewOptions{
				Name:   normalizeAccountName(values.Name),
				Values: values,
			}, nil
		},
		CreateOrUpdateAccount: func(ctx platform.AccountNewContext, opts platform.AccountNewOptions) error {
			values, ok := opts.Values.(AccountNewOptions)
			if !ok {
				return fmt.Errorf("invalid github account options")
			}
			accountConfig := AccountConfig{
				AppID:          values.AppID,
				InstallationID: values.InstallationID,
				PrivateKeyPath: values.PrivateKeyPath,
				BaseURL:        values.BaseURL,
				WebURL:         values.WebURL,
				Repositories:   values.Repositories,
			}
			if values.PollInterval > 0 {
				accountConfig.PollInterval = NewDuration(values.PollInterval)
			}
			if err := UpsertAccountConfig(ctx.Platform, opts.Name, accountConfig); err != nil {
				return err
			}
			acc, err := NewAccount(opts.Name, values.AppID, values.InstallationID, values.PrivateKeyPath, values.BaseURL)
			if err != nil {
				return err
			}
			if err := ctx.Platform.DataStore().SaveAccount(acc); err != nil {
				return fmt.Errorf("save account: %w", err)
			}
			fmt.Printf("✅ 已添加 GitHub 账户: %s (%s)\n", acc.Name, acc.ID)
			fmt.Println("Note: Configure platforms.github.accounts.<name>.mcp.command and .mcp.args before running this account.")
			return nil
		},
		DeleteAccount: func(ctx platform.AccountDeleteContext) error {
			return DeleteAccountConfig(ctx.Platform, ctx.Account.Name)
		},
		NewRuntimePlatform: func(ctx platform.RuntimeContext) (core.Platform, error) {
			githubConfig, err := LoadConfig(ctx.Platform)
			if err != nil {
				return nil, err
			}
			return NewPlatform(ctx.Account, githubConfig, ctx.Store), nil
		},
		CommandPolicy:       commands.DefaultPolicy(),
		TextChunkLimit:      0,
		EnableTextStreaming: false,
	}
}

func normalizeAccountNewIO(io platform.AccountNewIO) platform.AccountNewIO {
	if io.In == nil {
		io.In = os.Stdin
	}
	if io.Out == nil {
		io.Out = os.Stdout
	}
	return io
}
