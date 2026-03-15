// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import CoreGraphics
import Foundation

/// A single node in the server-driven view tree. Matches the Go `ui.Node` type.
struct ViewNode: Codable, Sendable {
    let type: String
    let id: String?
    let props: ViewProps?
    let children: [ViewNode]?

    /// Stable identity for SwiftUI.
    var stableId: String { id ?? type }

    /// Convenience accessor for children, never nil.
    var childNodes: [ViewNode] { children ?? [] }

    /// Assign deterministic path-based IDs to all nodes that don't have
    /// explicit IDs. This gives SwiftUI stable identity across re-renders
    /// so it can preserve text field focus, scroll position, etc.
    func withPathIDs(prefix: String = "") -> ViewNode {
        let myID = id ?? "\(prefix)\(type)"
        let newChildren = children?.enumerated().map { index, child in
            child.withPathIDs(prefix: "\(myID)/\(index)-")
        }
        return ViewNode(type: type, id: myID, props: props, children: newChildren)
    }
}

/// A dimension that can be a numeric value or `.infinity`.
enum FrameDimension: Codable, Sendable, Equatable {
    case value(Double)
    case infinity

    var cgFloat: CGFloat {
        switch self {
        case .value(let v): return CGFloat(v)
        case .infinity: return .infinity
        }
    }

    init(from decoder: Decoder) throws {
        let container = try decoder.singleValueContainer()
        if let s = try? container.decode(String.self), s == "infinity" {
            self = .infinity
        } else {
            self = .value(try container.decode(Double.self))
        }
    }

    func encode(to encoder: Encoder) throws {
        var container = encoder.singleValueContainer()
        switch self {
        case .value(let v): try container.encode(v)
        case .infinity: try container.encode("infinity")
        }
    }
}

/// Display and interaction properties for a view node.
/// Matches the Go `ui.Props` type with snake_case JSON keys.
struct ViewProps: Codable, Sendable {
    // Content
    let text: String?
    let placeholder: String?
    let sfSymbol: String?
    let imageAsset: String?
    let imageURL: String?

    // Typography
    let font: String?
    let weight: String?

    // Color and decoration
    let color: String?
    let bgColor: String?
    let cornerRadius: Double?
    let opacity: Double?

    // Layout
    let spacing: Int?
    let padding: [Int]?
    let minLength: Int?
    let alignment: String?
    let maxLines: Int?
    let truncate: String?

    // Navigation
    let title: String?
    let titleDisplayMode: String?

    // State
    let disabled: Bool?

    // Interaction
    let action: String?
    let style: String?

    // Input
    let keyboard: String?
    let autocorrect: Bool?
    let autocapitalize: String?
    let submitLabel: String?

    // Scroll
    let scrollAnchor: String?
    let scrollDismissKeyboard: String?
    let keyboardAvoidance: String?

    // Frame
    let frameWidth: Double?
    let frameHeight: Double?
    let frameMaxWidth: FrameDimension?
    let frameMaxHeight: FrameDimension?

    // Visual
    let foregroundStyle: String?
    let contentMode: String?

    // Accessibility
    let a11yLabel: String?

    enum CodingKeys: String, CodingKey {
        case text, placeholder, font, weight, color, opacity
        case spacing, padding, alignment, title, disabled, action, style
        case keyboard, autocorrect
        case sfSymbol = "sf_symbol"
        case imageAsset = "image_asset"
        case imageURL = "image_url"
        case bgColor = "bg_color"
        case cornerRadius = "corner_radius"
        case minLength = "min_length"
        case maxLines = "max_lines"
        case truncate
        case titleDisplayMode = "title_display_mode"
        case autocapitalize
        case submitLabel = "submit_label"
        case scrollAnchor = "scroll_anchor"
        case scrollDismissKeyboard = "scroll_dismiss_keyboard"
        case keyboardAvoidance = "keyboard_avoidance"
        case frameWidth = "frame_width"
        case frameHeight = "frame_height"
        case frameMaxWidth = "frame_max_width"
        case frameMaxHeight = "frame_max_height"
        case foregroundStyle = "foreground_style"
        case contentMode = "content_mode"
        case a11yLabel = "a11y_label"
    }
}

/// Wire message: server sends a view tree to the client.
struct ViewMessage: Codable, Sendable {
    let type: String
    let root: ViewNode
    let slot: String?
}

/// Wire message: server tells the client to dismiss a slot.
struct DismissMessage: Codable, Sendable {
    let type: String
    let slot: String
}

/// Wire message: client sends an action back to the server.
struct ActionMessage: Codable, Sendable {
    let type: String
    let action: String
    let value: String?
}
