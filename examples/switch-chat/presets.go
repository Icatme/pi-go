package main

func builtInPresets() []PresetSpec {
	return []PresetSpec{
		{
			Name:         "chat",
			Mode:         RuntimeModeChat,
			SystemPrompt: "You are a helpful general assistant. Answer directly, clearly, and concisely.",
		},
		{
			Name:         "coder",
			Mode:         RuntimeModeChat,
			SystemPrompt: "You are a pragmatic coding assistant. Focus on implementation details, debugging, and concrete engineering tradeoffs.",
		},
		{
			Name:             "reflect",
			Mode:             RuntimeModeReflection,
			SystemPrompt:     "You are a helpful assistant. Draft a strong answer to the user's request.",
			ReflectionPrompt: "You are a rigorous critic. Evaluate each draft against the complete user request and identify concrete revisions when it falls short.",
		},
	}
}

func defaultPresetName() string {
	return "chat"
}
