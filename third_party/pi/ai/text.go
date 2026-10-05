package ai

import "strings"

// Text helpers, ported from pi packages/ai/src/utils/text.ts (upstream
// 9e05370b2). All three are root-exported upstream.

// ContentText extracts and joins the text blocks of content (pi contentText).
// The separator defaults to "\n"; pass one to override it. Non-text blocks are
// skipped. pi also accepts a plain string, which is returned as is; a Go
// string-form content is a single text block, so it comes back unchanged too.
func ContentText(content ContentList, separator ...string) string {
	sep := "\n"
	if len(separator) > 0 {
		sep = separator[0]
	}
	var texts []string
	for _, block := range content {
		if text, ok := block.(TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	return strings.Join(texts, sep)
}

// GetSystemMessageText renders a system message as a complete prompt: its
// content followed by its section values, empty parts dropped, joined by a
// blank line (pi getSystemMessageText).
func GetSystemMessageText(message SystemMessage) string {
	parts := []string{ContentText(message.Content)}
	for _, section := range message.Sections.Entries() {
		if section.Value != nil {
			parts = append(parts, *section.Value)
		}
	}
	nonEmpty := parts[:0]
	for _, part := range parts {
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, "\n\n")
}

// RenderSystemMessageUpdate renders a later system message for APIs that accept
// system messages mid-conversation (pi renderSystemMessageUpdate). Section
// changes are framed by name so the model can relate them to the leading
// prompt. The framing is request-time only and may change between versions.
func RenderSystemMessageUpdate(message SystemMessage) string {
	var parts []string
	if text := ContentText(message.Content); text != "" {
		parts = append(parts, text)
	}
	for _, section := range message.Sections.Entries() {
		if section.Value == nil {
			parts = append(parts, `Removed system prompt section "`+section.Name+`".`)
		} else {
			parts = append(parts, `Updated system prompt section "`+section.Name+`":`+"\n\n"+*section.Value)
		}
	}
	return strings.Join(parts, "\n\n")
}
