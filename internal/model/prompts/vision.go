package prompts

// DefaultVisionPrompt is the default system prompt for vision analysis
// of image attachments. It instructs the model to describe the image
// concisely with attention to key subjects, text, and important details.
// Can be overridden via attachments.vision.prompt in config.
const DefaultVisionPrompt = "Describe this image concisely. Note the key subjects, any visible text, and important details."
