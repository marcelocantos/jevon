// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

struct ContentView: View {
    @Environment(Connection.self) private var connection
    @State private var showSheet = false
    @State private var showNotification = false

    var body: some View {
        Group {
            if let mainView = connection.mainView {
                // Server-driven UI — render the view tree from jevond.
                ServerView(node: mainView) { action, value in
                    connection.sendAction(action, value: value)
                }
            } else {
                // Fallback to purpose-built views.
                fallbackView
            }
        }
        .onChange(of: connection.sheetView != nil) { _, hasSheet in
            showSheet = hasSheet
        }
        .sheet(isPresented: $showSheet, onDismiss: {
            // If the user dismisses via swipe, tell the server.
            if connection.sheetView != nil {
                connection.sendAction("dismiss_sheet")
            }
        }) {
            if let sheetView = connection.sheetView {
                ServerView(node: sheetView) { action, value in
                    connection.sendAction(action, value: value)
                }
            }
        }
        .onChange(of: connection.notificationTitle != nil) { _, hasNotification in
            showNotification = hasNotification
        }
        .alert(
            connection.notificationTitle ?? "",
            isPresented: $showNotification,
            actions: { Button("OK") { connection.dismissNotification() } },
            message: { Text(connection.notificationBody ?? "") }
        )
    }

    @ViewBuilder
    private var fallbackView: some View {
        switch connection.state {
        case .disconnected:
            ConnectView()
        case .connecting:
            if connection.hasConnected {
                ChatView()
            } else {
                ProgressView("Connecting...")
            }
        case .connected:
            ChatView()
        case .error:
            if connection.hasConnected {
                ChatView()
            } else {
                ConnectView()
            }
        }
    }
}
