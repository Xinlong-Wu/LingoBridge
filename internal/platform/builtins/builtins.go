package builtins

import (
	"lingobridge/internal/platform"
	feishudefinition "lingobridge/internal/platform/feishu/definition"
	githubplatform "lingobridge/internal/platform/github"
	wechatplatform "lingobridge/internal/platform/wechat"
)

func NewRegistry() (*platform.Registry, error) {
	r := platform.NewRegistry()
	for _, def := range []platform.Definition{
		wechatplatform.Definition(),
		feishudefinition.Definition(),
		githubplatform.Definition(),
	} {
		if err := r.Register(def); err != nil {
			return nil, err
		}
	}
	return r, nil
}
