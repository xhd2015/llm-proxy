// Package anthropicthinking holds native Anthropic Messages thinking policy:
// which model ids require thinking.type=adaptive, and the in-place rewrite
// from classic thinking.type=enabled. It is not a protocol converter;
// pkgs/anthropic2openai imports it when the Responses bridge needs the same
// model classification.
package anthropicthinking
