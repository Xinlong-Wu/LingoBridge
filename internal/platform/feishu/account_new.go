package feishu

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"strings"
)

type AccountNewOptions struct {
	Name      string
	AppID     string
	AppSecret string
	BaseURL   string
}

func ParseAccountNewFlags(args []string, in io.Reader, out io.Writer) (AccountNewOptions, error) {
	fs := newAccountFlagSet("account new feishu")
	name := fs.String("name", "default", "account name")
	appID := fs.String("app-id", "", "Feishu app ID")
	appSecret := fs.String("app-secret", "", "Feishu app secret")
	baseURL := fs.String("base-url", "", "platform API base URL")
	if err := fs.Parse(args); err != nil {
		return AccountNewOptions{}, err
	}
	if fs.NArg() > 0 {
		return AccountNewOptions{}, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}

	reader := bufio.NewReader(in)
	var err error
	prompted := false
	if strings.TrimSpace(*appID) == "" {
		*appID, err = promptValue(reader, out, "飞书 App ID: ", true)
		if err != nil {
			return AccountNewOptions{}, err
		}
		prompted = true
	}
	if strings.TrimSpace(*appSecret) == "" {
		*appSecret, err = promptValue(reader, out, "飞书 App Secret: ", true)
		if err != nil {
			return AccountNewOptions{}, err
		}
		prompted = true
	}
	if prompted && strings.TrimSpace(*baseURL) == "" {
		*baseURL, err = promptValue(reader, out, "飞书 API Base URL（直接回车使用默认值）: ", false)
		if err != nil {
			return AccountNewOptions{}, err
		}
	}

	return AccountNewOptions{
		Name:      normalizeAccountName(*name),
		AppID:     *appID,
		AppSecret: *appSecret,
		BaseURL:   *baseURL,
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
