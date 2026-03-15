// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

/// Generic recursive renderer that maps server-driven ViewNode trees to SwiftUI.
struct ServerView: View {
    let node: ViewNode
    let onAction: (String, String) -> Void

    var body: some View {
        applyModifiers(to: renderNode())
    }

    // MARK: - Node dispatch

    @ViewBuilder
    private func renderNode() -> some View {
        switch node.type {
        case "text":
            renderText()
        case "vstack":
            renderVStack()
        case "hstack":
            renderHStack()
        case "zstack":
            renderZStack()
        case "spacer":
            Spacer(minLength: node.props?.minLength.map { CGFloat($0) })
        case "scroll":
            renderScroll()
        case "list":
            renderList()
        case "button":
            renderButton()
        case "icon_button":
            renderIconButton()
        case "text_field":
            ServerTextField(node: node, onAction: onAction)
        case "image":
            renderImage()
        case "nav":
            renderNav()
        case "badge":
            renderBadge()
        case "progress":
            renderProgress()
        case "padding":
            renderPaddingWrapper()
        case "background":
            renderBackgroundWrapper()
        case "tap":
            renderTap()
        default:
            // Unknown types render their children if any, or nothing.
            ForEach(indexedChildren()) { child in
                ServerView(node: child.node, onAction: onAction)
            }
        }
    }

    // MARK: - Text

    @ViewBuilder
    private func renderText() -> some View {
        Text(node.props?.text ?? "")
            .applyFont(node.props?.font, weight: node.props?.weight)
            .applyTruncation(node.props?.truncate)
    }

    // MARK: - Stacks

    @ViewBuilder
    private func renderVStack() -> some View {
        let spacing = node.props?.spacing.map { CGFloat($0) }
        let alignment = resolveHAlignment(node.props?.alignment)
        VStack(alignment: alignment, spacing: spacing) {
            ForEach(indexedChildren()) { child in
                ServerView(node: child.node, onAction: onAction)
            }
        }
    }

    @ViewBuilder
    private func renderHStack() -> some View {
        let spacing = node.props?.spacing.map { CGFloat($0) }
        HStack(spacing: spacing) {
            ForEach(indexedChildren()) { child in
                ServerView(node: child.node, onAction: onAction)
            }
        }
    }

    @ViewBuilder
    private func renderZStack() -> some View {
        ZStack {
            ForEach(indexedChildren()) { child in
                ServerView(node: child.node, onAction: onAction)
            }
        }
    }

    // MARK: - Scroll

    @ViewBuilder
    private func renderScroll() -> some View {
        let childCount = countDescendants(node)

        return ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(indexedChildren()) { child in
                        ServerView(node: child.node, onAction: onAction)
                            .id(child.id)
                    }
                    // Invisible anchor at the bottom for scrollTo.
                    Color.clear.frame(height: 1).id("__scroll_bottom__")
                }
            }
            .scrollDismissesKeyboard(.interactively)
            .defaultScrollAnchor(.bottom)
            .ignoresSafeArea(.keyboard)
            .onChange(of: childCount) {
                withAnimation(.easeOut(duration: 0.2)) {
                    proxy.scrollTo("__scroll_bottom__", anchor: .bottom)
                }
            }
            .onAppear {
                proxy.scrollTo("__scroll_bottom__", anchor: .bottom)
            }
        }
    }

    /// Count total descendants for change detection.
    private func countDescendants(_ n: ViewNode) -> Int {
        let children = n.children ?? []
        return children.count + children.reduce(0) { $0 + countDescendants($1) }
    }

    // MARK: - List

    @ViewBuilder
    private func renderList() -> some View {
        List {
            ForEach(indexedChildren()) { child in
                let swipeActions = child.node.childNodes.filter { $0.type == "swipe_action" }
                let displayChildren = child.node.childNodes.filter { $0.type != "swipe_action" }

                // If the child has non-swipe content, render it; otherwise render the child directly.
                Group {
                    if displayChildren.count != child.node.childNodes.count {
                        // Child had swipe actions — render its display children inline.
                        ServerView(
                            node: ViewNode(
                                type: child.node.type,
                                id: child.node.id,
                                props: child.node.props,
                                children: displayChildren.isEmpty ? nil : displayChildren
                            ),
                            onAction: onAction
                        )
                    } else {
                        ServerView(node: child.node, onAction: onAction)
                    }
                }
                .swipeActions(edge: .trailing, allowsFullSwipe: false) {
                    ForEach(Array(swipeActions.enumerated()), id: \.offset) { _, action in
                        if let actionId = action.props?.action {
                            Button(role: action.props?.style == "destructive" ? .destructive : nil) {
                                onAction(actionId, "")
                            } label: {
                                if let symbol = action.props?.sfSymbol {
                                    Label(action.props?.text ?? "", systemImage: symbol)
                                } else {
                                    Text(action.props?.text ?? "")
                                }
                            }
                        }
                    }
                }
            }
        }
    }

    // MARK: - Buttons

    @ViewBuilder
    private func renderButton() -> some View {
        let action = node.props?.action ?? ""
        let style = node.props?.style
        let callback = self.onAction

        Button(role: style == "destructive" ? .destructive : nil) {
            callback(action, "")
        } label: {
            if let symbol = node.props?.sfSymbol {
                Label(node.props?.text ?? "", systemImage: symbol)
            } else {
                Text(node.props?.text ?? "")
            }
        }
    }

    @ViewBuilder
    private func renderIconButton() -> some View {
        let action = node.props?.action ?? ""
        let callback = self.onAction

        Button {
            callback(action, "")
        } label: {
            if let symbol = node.props?.sfSymbol {
                Image(systemName: symbol)
            } else {
                Text(node.props?.text ?? "")
            }
        }
    }

    // MARK: - Image

    @ViewBuilder
    private func renderImage() -> some View {
        if let symbol = node.props?.sfSymbol {
            Image(systemName: symbol)
        } else if let asset = node.props?.imageAsset {
            Image(asset)
                .renderingMode(.template)
        } else if let urlString = node.props?.imageURL {
            if urlString.hasPrefix("data:") {
                if let data = decodeDataURI(urlString),
                   let uiImage = UIImage(data: data) {
                    Image(uiImage: uiImage)
                        .resizable()
                        .aspectRatio(contentMode: .fit)
                } else {
                    Image(systemName: "photo")
                }
            } else if let url = URL(string: urlString) {
                AsyncImage(url: url) { image in
                    image.resizable().aspectRatio(contentMode: .fit)
                } placeholder: {
                    ProgressView()
                }
            }
        }
    }

    // MARK: - Nav

    @ViewBuilder
    private func renderNav() -> some View {
        let title = node.props?.title ?? ""
        // Toolbar items can be direct children or nested inside a "toolbar" node.
        let allChildren = node.childNodes
        let toolbarNodes = allChildren.filter { $0.type == "toolbar" }
        let toolbarChildren = toolbarNodes.flatMap { $0.childNodes } + allChildren.filter {
            $0.type == "toolbar_leading" || $0.type == "toolbar_trailing"
        }
        let leading = toolbarChildren.filter { $0.type == "toolbar_leading" }
        let trailing = toolbarChildren.filter { $0.type == "toolbar_trailing" }
        let content = allChildren.filter {
            $0.type != "toolbar_leading" && $0.type != "toolbar_trailing" && $0.type != "toolbar"
        }

        NavigationStack {
            VStack(spacing: 0) {
                ForEach(indexed(content)) { child in
                    ServerView(node: child.node, onAction: onAction)
                }
            }
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    HStack(spacing: 8) {
                        ForEach(indexed(leading.flatMap { $0.childNodes })) { child in
                            ServerView(node: child.node, onAction: onAction)
                        }
                    }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    HStack(spacing: 8) {
                        ForEach(indexed(trailing.flatMap { $0.childNodes })) { child in
                            ServerView(node: child.node, onAction: onAction)
                        }
                    }
                }
            }
        }
    }

    // MARK: - Badge

    @ViewBuilder
    private func renderBadge() -> some View {
        let bgColor = resolveColor(node.props?.bgColor)
        Text(node.props?.text ?? "")
            .font(.caption2.weight(.semibold))
            .textCase(.uppercase)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(bgColor.opacity(0.15))
            .foregroundStyle(bgColor)
            .clipShape(Capsule())
    }

    // MARK: - Progress

    @ViewBuilder
    private func renderProgress() -> some View {
        if let text = node.props?.text {
            HStack(spacing: 6) {
                ProgressView()
                    .scaleEffect(0.7)
                Text(text)
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
        } else {
            ProgressView()
        }
    }

    // MARK: - Padding wrapper

    @ViewBuilder
    private func renderPaddingWrapper() -> some View {
        ForEach(indexedChildren()) { child in
            ServerView(node: child.node, onAction: onAction)
        }
        .applyPaddingArray(node.props?.padding)
    }

    // MARK: - Background wrapper

    @ViewBuilder
    private func renderBackgroundWrapper() -> some View {
        let bgColor = resolveColor(node.props?.bgColor)
        let radius = node.props?.cornerRadius ?? 0

        ForEach(indexedChildren()) { child in
            ServerView(node: child.node, onAction: onAction)
        }
        .background(bgColor, in: RoundedRectangle(cornerRadius: radius))
    }

    // MARK: - Tap

    @ViewBuilder
    private func renderTap() -> some View {
        let action = node.props?.action ?? ""
        let callback = self.onAction

        ForEach(indexedChildren()) { child in
            ServerView(node: child.node, onAction: onAction)
        }
        .onTapGesture {
            callback(action, "")
        }
    }

    // MARK: - Common modifiers

    @ViewBuilder
    private func applyModifiers<V: View>(to view: V) -> some View {
        view
            .applyForegroundColor(node.props?.color)
            .applyLineLimit(node.props?.maxLines)
            .applyOpacity(node.props?.opacity)
            .applyDisabled(node.props?.disabled)
    }

    // MARK: - Helpers

    private func indexedChildren() -> [IndexedNode] {
        indexed(node.childNodes)
    }

    private func indexed(_ nodes: [ViewNode]) -> [IndexedNode] {
        nodes.enumerated().map { index, child in
            IndexedNode(node: child, index: index)
        }
    }
}

// MARK: - Client-owned text field

/// Text field with client-owned state. The server never overwrites what the user is typing.
private struct ServerTextField: View {
    let node: ViewNode
    let onAction: (String, String) -> Void
    @State private var text: String = ""

    var body: some View {
        let action = node.props?.action ?? ""

        HStack(spacing: 8) {
            TextField(
                node.props?.placeholder ?? "",
                text: $text,
                axis: .vertical
            )
            .textFieldStyle(.roundedBorder)
            .lineLimit(1...5)
            .autocorrectionDisabled()
            .textInputAutocapitalization(.sentences)
            .onSubmit { submit(action: action) }

            Button {
                submit(action: action)
            } label: {
                Image(systemName: "arrow.up.circle.fill")
                    .font(.title2)
            }
            .disabled(text.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
        }
    }

    private func submit(action: String) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty else { return }
        onAction(action, trimmed)
        text = ""
    }
}

// MARK: - Indexed node wrapper for ForEach identity

private struct IndexedNode: Identifiable {
    let node: ViewNode
    let index: Int

    var id: String {
        node.id ?? "\(node.type)-\(index)"
    }
}

// MARK: - Color resolution

private func resolveColor(_ name: String?) -> Color {
    guard let name else { return .primary }
    switch name {
    case "blue": return .blue
    case "red": return .red
    case "green": return .green
    case "orange": return .orange
    case "yellow": return .yellow
    case "purple": return .purple
    case "pink": return .pink
    case "white": return .white
    case "black": return .black
    case "gray": return Color(.systemGray5)
    case "secondary": return .secondary
    case "primary": return .primary
    case "clear": return .clear
    case "bar": return Color(.systemBackground)
    default: return .primary
    }
}

// MARK: - Alignment resolution

private func resolveHAlignment(_ name: String?) -> HorizontalAlignment {
    switch name {
    case "leading": return .leading
    case "trailing": return .trailing
    case "center": return .center
    default: return .center
    }
}

// MARK: - Data URI decoding

private func decodeDataURI(_ uri: String) -> Data? {
    // Format: data:[<mediatype>][;base64],<data>
    guard let commaIndex = uri.firstIndex(of: ",") else { return nil }
    let meta = uri[uri.startIndex..<commaIndex]
    let encoded = String(uri[uri.index(after: commaIndex)...])

    if meta.contains(";base64") {
        return Data(base64Encoded: encoded)
    } else {
        return encoded.removingPercentEncoding.map { Data($0.utf8) }
    }
}

// MARK: - View extensions for optional modifiers

private extension View {
    @ViewBuilder
    func applyFont(_ font: String?, weight: String?) -> some View {
        let resolvedFont = resolveFont(font)
        let resolvedWeight = resolveWeight(weight)
        if let resolvedFont, let resolvedWeight {
            self.font(resolvedFont.weight(resolvedWeight))
        } else if let resolvedFont {
            self.font(resolvedFont)
        } else if let resolvedWeight {
            self.fontWeight(resolvedWeight)
        } else {
            self
        }
    }

    @ViewBuilder
    func applyForegroundColor(_ name: String?) -> some View {
        if let name {
            self.foregroundStyle(resolveColor(name))
        } else {
            self
        }
    }

    @ViewBuilder
    func applyLineLimit(_ maxLines: Int?) -> some View {
        if let maxLines {
            self.lineLimit(maxLines)
        } else {
            self
        }
    }

    @ViewBuilder
    func applyOpacity(_ opacity: Double?) -> some View {
        if let opacity {
            self.opacity(opacity)
        } else {
            self
        }
    }

    @ViewBuilder
    func applyDisabled(_ disabled: Bool?) -> some View {
        if let disabled, disabled {
            self.disabled(true)
        } else {
            self
        }
    }

    @ViewBuilder
    func applyTruncation(_ truncate: String?) -> some View {
        switch truncate {
        case "head": self.truncationMode(.head)
        case "middle": self.truncationMode(.middle)
        case "tail": self.truncationMode(.tail)
        default: self
        }
    }

    @ViewBuilder
    func applyPaddingArray(_ padding: [Int]?) -> some View {
        if let padding, !padding.isEmpty {
            switch padding.count {
            case 1:
                self.padding(CGFloat(padding[0]))
            case 2:
                self.padding(.horizontal, CGFloat(padding[0]))
                    .padding(.vertical, CGFloat(padding[1]))
            case 4:
                self.padding(EdgeInsets(
                    top: CGFloat(padding[0]),
                    leading: CGFloat(padding[3]),
                    bottom: CGFloat(padding[2]),
                    trailing: CGFloat(padding[1])
                ))
            default:
                self.padding(CGFloat(padding[0]))
            }
        } else {
            self
        }
    }
}

// MARK: - Font/weight resolution

private func resolveFont(_ name: String?) -> Font? {
    switch name {
    case "body": return .body
    case "caption": return .caption
    case "caption2": return .caption2
    case "title": return .title
    case "title2": return .title2
    case "title3": return .title3
    case "headline": return .headline
    case "callout": return .callout
    case "footnote": return .footnote
    case "monospaced": return .body.monospaced()
    default: return nil
    }
}

private func resolveWeight(_ name: String?) -> Font.Weight? {
    switch name {
    case "medium": return .medium
    case "semibold": return .semibold
    case "bold": return .bold
    case "regular": return .regular
    case "light": return .light
    default: return nil
    }
}
