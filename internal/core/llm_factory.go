package core

import (
	"lingobridge/internal/config"
	"lingobridge/internal/llm"
)

func defaultLLMFactory(model config.ResolvedModel) llm.Client {
	return llm.NewClient(llm.Config{
		Provider: model.Provider,
		BaseURL:  model.BaseURL,
		APIKey:   model.APIKey,
		Model:    model.ID,
		Endpoint: model.Endpoint,
		Compact: llm.CompactConfig{
			Mode:          string(model.Compact.Mode),
			ContextWindow: model.ContextWindow,
			Threshold:     model.Compact.Threshold,
			Instructions:  model.Compact.Instructions,
		},
	})
}

func (b *Bot) llmForUser(userID string) (config.ResolvedModel, llm.Client, error) {
	modelName, err := b.Sessions.CurrentModel(userID)
	if err != nil {
		return config.ResolvedModel{}, nil, err
	}
	model, err := b.LLMConfig.ResolveModel(modelName)
	if err != nil {
		model, err = b.LLMConfig.ResolveModel(b.LLMConfig.DefaultModel)
		if err != nil {
			return config.ResolvedModel{}, nil, err
		}
	}
	newLLM := b.NewLLM
	if newLLM == nil {
		newLLM = defaultLLMFactory
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.LLMClients == nil {
		b.LLMClients = map[string]llm.Client{}
	}
	client, ok := b.LLMClients[model.Name]
	if !ok {
		client = newLLM(model)
		b.LLMClients[model.Name] = client
	}
	return model, client, nil
}
