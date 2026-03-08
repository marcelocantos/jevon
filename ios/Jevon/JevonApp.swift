// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

@main
struct JevonApp: App {
    @State private var connection = Connection()

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environment(connection)
                .task {
                    #if targetEnvironment(simulator)
                    connection.connect(to: "localhost", port: 13705)
                    #endif
                }
        }
    }
}
