package gateway

const defaultCodarImageModel = "gemini-nano-banana-2.1"

// The official desktop client's image_gen tool schema declares these IDs.
// /v1/codar/models is the separate conversation-model catalog.
var codarImageModels = []struct{ id, name string }{
	{defaultCodarImageModel, "Nano Banana 2.1"},
	{"gpt-image-2.5", "GPT Image 2.5"},
	{"gpt-image-2", "GPT Image 2"},
	{"gemini-3-pro-image", "Gemini 3 Pro Image"},
	{"gemini-3-pro-image-preview", "Gemini 3 Pro Image Preview"},
}

func isCodarImageModel(id string) bool {
	for _, model := range codarImageModels {
		if model.id == id {
			return true
		}
	}
	return false
}

func catalogNeedsChat(key M) bool {
	ids := arr(key["models"])
	if len(ids) == 0 {
		return true
	}
	for _, id := range ids {
		if !isCodarImageModel(text(id)) {
			return true
		}
	}
	return false
}

func imageModelCatalog(chat []any) []any {
	out := make([]any, 0, len(chat)+len(codarImageModels))
	for _, model := range chat {
		if !isCodarImageModel(text(obj(model)["id"])) {
			out = append(out, model)
		}
	}
	for _, model := range codarImageModels {
		out = append(out, M{"id": model.id, "name": model.name, "type": "image", "protocol": "openai_images",
			"input_modalities": []any{"text", "image"}, "output_modalities": []any{"image"}, "catalog_source": "image_gen"})
	}
	return out
}
