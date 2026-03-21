// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

@main
struct JevonApp: App {
    @State private var connection = Connection()
    @State private var voiceManager = VoiceManager()
    @State private var showSafeMode = false

    var body: some Scene {
        WindowGroup {
            ContentView()
                .environment(connection)
                .environment(voiceManager)
                .task {
                    #if targetEnvironment(simulator)
                    connection.connect(to: "localhost", port: 13705)
                    #endif
                }
                .onChange(of: connection.httpBaseURL) { _, url in
                    voiceManager.serverBaseURL = url
                }
                .task {
                    voiceManager.onUtterance = { text in
                        connection.send(text)
                    }
                }
                .onAppear {
                    installChevronGesture()
                }
                .sheet(isPresented: $showSafeMode) {
                    SafeModeView()
                        .environment(connection)
                }
        }
    }

    /// Install the two-finger chevron gesture recogniser at the UIWindow level.
    private func installChevronGesture() {
        DispatchQueue.main.async {
            guard let scene = UIApplication.shared.connectedScenes.first as? UIWindowScene,
                  let window = scene.windows.first else {
                return
            }

            // Avoid duplicate installation.
            if window.gestureRecognizers?.contains(where: { $0 is ChevronGestureRecognizer }) == true {
                return
            }

            let target = SafeModeTarget { showSafeMode = true }
            let recognizer = ChevronGestureRecognizer(
                target: target,
                action: #selector(SafeModeTarget.chevronDetected(_:))
            )

            // Retain the target via associated object on the window.
            objc_setAssociatedObject(window, &SafeModeTarget.key, target, .OBJC_ASSOCIATION_RETAIN)

            window.addGestureRecognizer(recognizer)
        }
    }
}

/// Objective-C target for the chevron gesture recogniser.
/// Stored as an associated object on the window to prevent deallocation.
final class SafeModeTarget: NSObject {
    nonisolated(unsafe) static var key: UInt8 = 0
    private let handler: () -> Void

    init(handler: @escaping () -> Void) {
        self.handler = handler
    }

    @objc func chevronDetected(_ recognizer: UIGestureRecognizer) {
        if recognizer.state == .ended {
            handler()
        }
    }
}
