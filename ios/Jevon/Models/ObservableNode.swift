// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import Foundation
import Observation

/// A persistent, observable node in the server-driven UI tree.
///
/// Unlike `ViewNode` (a value type rebuilt every render), `ObservableNode`
/// persists across renders. The Lua reconciler emits patches that mutate
/// existing nodes, so SwiftUI only re-renders what actually changed.
@Observable
@MainActor
final class ObservableNode: Identifiable {
    let id: String
    var type: String
    var props: ViewProps?
    var children: [ObservableNode]

    /// Quick child lookup by ID.
    private var childIndex: [String: Int] = [:]

    init(id: String, type: String, props: ViewProps?, children: [ObservableNode] = []) {
        self.id = id
        self.type = type
        self.props = props
        self.children = children
        rebuildIndex()
    }

    /// Build from a ViewNode tree (used for initial render / insert).
    convenience init(from viewNode: ViewNode) {
        let childNodes = (viewNode.children ?? []).map { ObservableNode(from: $0) }
        self.init(
            id: viewNode.id ?? viewNode.type,
            type: viewNode.type,
            props: viewNode.props,
            children: childNodes
        )
    }

    /// Convenience accessor matching ViewNode API.
    var childNodes: [ObservableNode] { children }

    // MARK: - Child management

    func child(byID id: String) -> ObservableNode? {
        guard let idx = childIndex[id], idx < children.count else { return nil }
        let child = children[idx]
        // Verify — index may be stale if mutations happened without rebuild.
        if child.id == id { return child }
        // Fallback: linear scan + rebuild.
        rebuildIndex()
        guard let idx2 = childIndex[id] else { return nil }
        return children[idx2]
    }

    /// Find a node by ID anywhere in the subtree.
    func find(_ id: String) -> ObservableNode? {
        if self.id == id { return self }
        for child in children {
            if let found = child.find(id) { return found }
        }
        return nil
    }

    /// Find the parent of a node with the given ID.
    func findParent(of id: String) -> ObservableNode? {
        for child in children {
            if child.id == id { return self }
            if let found = child.findParent(of: id) { return found }
        }
        return nil
    }

    func insertChild(_ child: ObservableNode, at index: Int) {
        let clamped = min(index, children.count)
        children.insert(child, at: clamped)
        rebuildIndex()
    }

    func removeChild(byID id: String) {
        children.removeAll { $0.id == id }
        rebuildIndex()
    }

    func reorderChildren(ids: [String]) {
        var byID: [String: ObservableNode] = [:]
        for child in children {
            byID[child.id] = child
        }
        var reordered: [ObservableNode] = []
        for id in ids {
            if let child = byID[id] {
                reordered.append(child)
            }
        }
        // Append any children not in the reorder list (shouldn't happen
        // with well-formed patches, but defensive).
        let reorderedSet = Set(ids)
        for child in children where !reorderedSet.contains(child.id) {
            reordered.append(child)
        }
        children = reordered
        rebuildIndex()
    }

    private func rebuildIndex() {
        childIndex = [:]
        for (i, child) in children.enumerated() {
            childIndex[child.id] = i
        }
    }
}

// MARK: - Patch types

/// A patch operation produced by the Lua reconciler.
enum NodePatch {
    /// First render — full tree.
    case initialize(ViewNode)
    /// Update props on an existing node.
    case update(id: String, props: ViewProps)
    /// Insert a new subtree.
    case insert(parentID: String, index: Int, node: ViewNode)
    /// Remove a node and its subtree.
    case remove(id: String)
    /// Replace a node (type changed).
    case replace(id: String, node: ViewNode)
    /// Reorder children of a node.
    case reorder(parentID: String, ids: [String])
}

// MARK: - Patch application

extension ObservableNode {
    /// Apply a list of patches to this tree (must be the root).
    func applyPatches(_ patches: [NodePatch]) {
        for patch in patches {
            switch patch {
            case .update(let id, let newProps):
                if let node = find(id) {
                    node.props = newProps
                }

            case .insert(let parentID, let index, let viewNode):
                if let parent = find(parentID) {
                    let newChild = ObservableNode(from: viewNode)
                    parent.insertChild(newChild, at: index)
                }

            case .remove(let id):
                if let parent = findParent(of: id) {
                    parent.removeChild(byID: id)
                }

            case .replace(let id, let viewNode):
                if let parent = findParent(of: id) {
                    let newNode = ObservableNode(from: viewNode)
                    if let idx = parent.children.firstIndex(where: { $0.id == id }) {
                        parent.children[idx] = newNode
                        parent.rebuildIndex()
                    }
                }

            case .reorder(let parentID, let ids):
                if let parent = find(parentID) {
                    parent.reorderChildren(ids: ids)
                }

            case .initialize:
                // Handled at the call site — root replacement.
                break
            }
        }
    }
}
