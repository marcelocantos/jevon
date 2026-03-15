// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

// Package ui defines the server-driven UI schema. View trees are built
// from primitive nodes (text, stacks, images, etc.) — no domain-specific
// components. Compositions like "chat bubble" are expressed as nested
// primitives, defined in Lua scripts on the server.
package ui

// Node is a single element in the view tree.
type Node struct {
	Type     string `json:"type"`
	ID       string `json:"id,omitempty"`
	Props    Props  `json:"props,omitzero"`
	Children []Node `json:"children,omitempty"`
}

// Props holds display and interaction properties. Fields are omitted
// when zero-valued. Not every field applies to every node type.
type Props struct {
	// Content
	Text        string `json:"text,omitempty"`
	Placeholder string `json:"placeholder,omitempty"`
	SFSymbol    string `json:"sf_symbol,omitempty"`    // SF Symbol name
	ImageAsset  string `json:"image_asset,omitempty"`  // bundled asset
	ImageURL    string `json:"image_url,omitempty"`    // URL or data: URI

	// Typography
	Font   string `json:"font,omitempty"`   // body, caption, caption2, title, title2, title3, headline, monospaced
	Weight string `json:"weight,omitempty"` // medium, semibold, bold

	// Color and decoration
	Color        string  `json:"color,omitempty"`         // text/icon color
	BgColor      string  `json:"bg_color,omitempty"`      // background color
	CornerRadius float64 `json:"corner_radius,omitempty"` // clip shape
	Opacity      float64 `json:"opacity,omitempty"`       // 0-1

	// Layout
	Spacing   int    `json:"spacing,omitempty"`
	Padding   []int  `json:"padding,omitempty"`    // [all] or [h,v] or [top,trailing,bottom,leading]
	MinLength int    `json:"min_length,omitempty"` // spacer
	Alignment string `json:"alignment,omitempty"`  // leading, trailing, center
	MaxLines  int    `json:"max_lines,omitempty"`  // line limit
	Truncate  string `json:"truncate,omitempty"`   // head, middle, tail

	// Navigation
	Title            string `json:"title,omitempty"`
	TitleDisplayMode string `json:"title_display_mode,omitempty"` // inline, large, automatic

	// State
	Disabled bool `json:"disabled,omitempty"`

	// Interaction — action ID sent back to server on user interaction
	Action string `json:"action,omitempty"`
	Style  string `json:"style,omitempty"` // destructive, cancel, default

	// Input
	Keyboard       string `json:"keyboard,omitempty"`        // default, email, url, number, phone, ascii, decimal
	Autocorrect    *bool  `json:"autocorrect,omitempty"`     // nil = system default
	Autocapitalize string `json:"autocapitalize,omitempty"`  // none, words, sentences, all
	SubmitLabel    string `json:"submit_label,omitempty"`    // return, done, send, search, go, next

	// Scroll
	ScrollAnchor         string `json:"scroll_anchor,omitempty"`          // top, bottom
	ScrollDismissKeyboard string `json:"scroll_dismiss_keyboard,omitempty"` // interactive, immediately, never
	KeyboardAvoidance    string `json:"keyboard_avoidance,omitempty"`     // avoid, ignore

	// Frame
	FrameWidth    float64 `json:"frame_width,omitempty"`
	FrameHeight   float64 `json:"frame_height,omitempty"`
	FrameMaxWidth  any    `json:"frame_max_width,omitempty"`  // number or "infinity"
	FrameMaxHeight any    `json:"frame_max_height,omitempty"` // number or "infinity"

	// Visual
	ForegroundStyle string `json:"foreground_style,omitempty"` // primary, secondary, tertiary, quaternary
	ContentMode     string `json:"content_mode,omitempty"`     // fit, fill

	// Accessibility
	A11yLabel string `json:"a11y_label,omitempty"`
}

// --- Wire messages ---

// ViewMessage sends a view tree to the client.
type ViewMessage struct {
	Type string `json:"type"`           // "view"
	Root Node   `json:"root"`           // the view tree
	Slot string `json:"slot,omitempty"` // "" = main screen, "sheet" = modal
}

// DismissMessage tells the client to close a slot.
type DismissMessage struct {
	Type string `json:"type"` // "dismiss"
	Slot string `json:"slot"` // which slot to dismiss
}

// ActionMessage is sent from client to server on user interaction.
type ActionMessage struct {
	Type   string `json:"type"`           // "action"
	Action string `json:"action"`         // action ID from node props
	Value  string `json:"value,omitempty"` // text input value, etc.
}
