package github

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"strings"
	"time"
)

type AccountNewOptions struct {
	Name           string
	AppID          string
	InstallationID string
	PrivateKeyPath string
	BaseURL        string
	WebURL         string
	PollInterval   time.Duration
	Repositories   []string
}

type repositoryFlags []string

func (f *repositoryFlags) String() string {
	return strings.Join(*f, ",")
}

func (f *repositoryFlags) Set(value string) error {
	repo, err := ParseRepository(value)
	if err != nil {
		return err
	}
	*f = append(*f, repo.FullName())
	return nil
}

func ParseAccountNewFlags(args []string, in io.Reader, out io.Writer) (AccountNewOptions, error) {
	fs := newAccountFlagSet("account new github")
	name := fs.String("name", "default", "account name")
	appID := fs.String("app-id", "", "GitHub App ID")
	installationID := fs.String("installation-id", "", "GitHub App installation ID")
	privateKeyPath := fs.String("private-key-path", "", "GitHub App PEM private key path")
	baseURL := fs.String("base-url", "", "GitHub API base URL")
	webURL := fs.String("web-url", "", "GitHub web URL")
	pollInterval := fs.String("poll-interval", "", "poll interval, for example 2m")
	var repos repositoryFlags
	fs.Var(&repos, "repo", "GitHub repository in owner/repo form; repeatable")
	if err := fs.Parse(args); err != nil {
		return AccountNewOptions{}, err
	}
	if fs.NArg() > 0 {
		return AccountNewOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	reader := bufio.NewReader(in)
	var err error
	if strings.TrimSpace(*appID) == "" {
		*appID, err = promptValue(reader, out, "GitHub App ID: ", true)
		if err != nil {
			return AccountNewOptions{}, err
		}
	}
	if strings.TrimSpace(*installationID) == "" {
		*installationID, err = promptValue(reader, out, "GitHub Installation ID: ", true)
		if err != nil {
			return AccountNewOptions{}, err
		}
	}
	if strings.TrimSpace(*privateKeyPath) == "" {
		*privateKeyPath, err = promptValue(reader, out, "GitHub App PEM private key path: ", true)
		if err != nil {
			return AccountNewOptions{}, err
		}
	}
	if len(repos) == 0 {
		rawRepos, err := promptValue(reader, out, "GitHub repositories (owner/repo, comma-separated): ", true)
		if err != nil {
			return AccountNewOptions{}, err
		}
		for _, item := range strings.Split(rawRepos, ",") {
			if err := repos.Set(item); err != nil {
				return AccountNewOptions{}, err
			}
		}
	}
	var poll time.Duration
	if strings.TrimSpace(*pollInterval) != "" {
		poll, err = time.ParseDuration(strings.TrimSpace(*pollInterval))
		if err != nil {
			return AccountNewOptions{}, fmt.Errorf("parse --poll-interval: %w", err)
		}
	}

	return AccountNewOptions{
		Name:           normalizeAccountName(*name),
		AppID:          *appID,
		InstallationID: *installationID,
		PrivateKeyPath: *privateKeyPath,
		BaseURL:        *baseURL,
		WebURL:         *webURL,
		PollInterval:   poll,
		Repositories:   normalizeRepositoryList(repos),
	}, nil
}

func newAccountFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	return fs
}

func normalizeAccountName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "default"
	}
	return name
}

func promptValue(reader *bufio.Reader, out io.Writer, prompt string, required bool) (string, error) {
	for {
		fmt.Fprint(out, prompt)
		value, err := reader.ReadString('\n')
		if err != nil && err != io.EOF {
			return "", err
		}
		value = strings.TrimSpace(value)
		if value != "" || !required {
			return value, nil
		}
		fmt.Fprintln(out, "此项必填，请重新输入。")
		if err == io.EOF {
			return "", fmt.Errorf("missing required input")
		}
	}
}
