// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import SwiftUI

struct ConnectView: View {
    @Environment(Connection.self) private var connection
    @State private var host: String = ""
    @State private var portText: String = "13705"

    var body: some View {
        NavigationStack {
            VStack(spacing: 24) {
                Spacer()

                Image(systemName: "terminal")
                    .font(.system(size: 64))
                    .foregroundStyle(.secondary)

                Text("Connect to Jevon")
                    .font(.title)

                if case .error(let msg) = connection.state {
                    Text(msg)
                        .foregroundStyle(.red)
                        .font(.callout)
                }

                VStack(spacing: 12) {
                    TextField("Host (e.g. 192.168.1.10)", text: $host)
                        .textFieldStyle(.roundedBorder)
                        .textContentType(.URL)
                        .autocorrectionDisabled()
                        .textInputAutocapitalization(.never)

                    TextField("Port", text: $portText)
                        .textFieldStyle(.roundedBorder)
                        .keyboardType(.numberPad)
                }
                .padding(.horizontal, 40)

                Button("Connect") {
                    let port = Int(portText) ?? 8080
                    connection.connect(to: host, port: port)
                }
                .buttonStyle(.borderedProminent)
                .disabled(host.isEmpty)

                Spacer()
                Spacer()
            }
            .navigationTitle("Jevon")
            .navigationBarTitleDisplayMode(.inline)
        }
        .onAppear {
            if let last = connection.lastServer {
                host = last.host
                portText = String(last.port)
            }
        }
    }
}
