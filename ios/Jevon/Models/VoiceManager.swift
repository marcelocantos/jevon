// Copyright 2026 Marcelo Cantos
// SPDX-License-Identifier: Apache-2.0

import AVFoundation
import Foundation
import Observation
import os

private let logger = Logger(subsystem: "com.marcelocantos.jevon", category: "VoiceManager")

/// Manages full-duplex voice input: local VAD → OpenAI Realtime API → transcription.
///
/// Flow:
/// 1. Always monitors mic levels locally (no network cost).
/// 2. On speech detection, requests an ephemeral token from jevond and
///    opens a WebSocket to OpenAI's Realtime API.
/// 3. Streams 24kHz mono PCM16 audio, receives transcription deltas.
/// 4. On utterance complete, calls onUtterance with the final text.
/// 5. On extended silence, closes the OpenAI connection.
@Observable
@MainActor
final class VoiceManager {
    enum State: Sendable {
        case idle           // Not listening
        case listening      // Local VAD active, waiting for speech
        case connecting     // Speech detected, getting token
        case streaming      // Connected to OpenAI, streaming audio
        case error(String)
    }

    private(set) var state: State = .idle
    /// Current partial transcription (for display).
    private(set) var partialText: String = ""

    /// Called when an utterance is complete.
    var onUtterance: ((String) -> Void)?

    /// Base URL for jevond (to request ephemeral tokens).
    var serverBaseURL: URL?

    // MARK: - Audio engine

    private var audioEngine: AVAudioEngine?
    private var webSocket: URLSessionWebSocketTask?
    private var silenceTimer: Task<Void, Never>?

    /// RMS threshold for local VAD speech detection.
    private let speechThreshold: Float = 0.02
    /// Seconds of silence before closing the OpenAI connection.
    private let silenceTimeout: TimeInterval = 3.0
    /// Whether we're currently detecting speech in the audio stream.
    private var isSpeaking = false

    // MARK: - Public API

    /// Start listening. Begins local VAD monitoring.
    func startListening() {
        guard case .idle = state else { return }

        do {
            let session = AVAudioSession.sharedInstance()
            try session.setCategory(.playAndRecord, mode: .default,
                                    options: [.defaultToSpeaker, .allowBluetooth])
            try session.setActive(true)
        } catch {
            state = .error("Audio session: \(error.localizedDescription)")
            return
        }

        let engine = AVAudioEngine()
        let inputNode = engine.inputNode
        let inputFormat = inputNode.outputFormat(forBus: 0)

        // Install a tap to monitor audio levels.
        inputNode.installTap(onBus: 0, bufferSize: 4096, format: inputFormat) {
            [weak self] buffer, _ in
            Task { @MainActor [weak self] in
                self?.processAudioBuffer(buffer)
            }
        }

        do {
            try engine.start()
        } catch {
            state = .error("Audio engine: \(error.localizedDescription)")
            return
        }

        audioEngine = engine
        state = .listening
        logger.info("Voice: listening (local VAD)")
    }

    /// Stop listening entirely.
    func stopListening() {
        closeOpenAI()
        audioEngine?.inputNode.removeTap(onBus: 0)
        audioEngine?.stop()
        audioEngine = nil
        state = .idle
        partialText = ""
        logger.info("Voice: stopped")
    }

    /// Toggle listening on/off.
    func toggle() {
        switch state {
        case .idle:
            startListening()
        default:
            stopListening()
        }
    }

    // MARK: - Local VAD

    private func processAudioBuffer(_ buffer: AVAudioPCMBuffer) {
        let rms = calculateRMS(buffer)

        switch state {
        case .listening:
            if rms > speechThreshold {
                isSpeaking = true
                connectToOpenAI()
            }

        case .streaming:
            // Reset silence timer on speech.
            if rms > speechThreshold {
                isSpeaking = true
                resetSilenceTimer()
            }
            // Send audio to OpenAI.
            sendAudioToOpenAI(buffer)

        default:
            break
        }
    }

    private func calculateRMS(_ buffer: AVAudioPCMBuffer) -> Float {
        guard let channelData = buffer.floatChannelData else { return 0 }
        let count = Int(buffer.frameLength)
        guard count > 0 else { return 0 }

        var sum: Float = 0
        for i in 0..<count {
            let sample = channelData[0][i]
            sum += sample * sample
        }
        return sqrt(sum / Float(count))
    }

    // MARK: - OpenAI Realtime connection

    private func connectToOpenAI() {
        guard let serverBaseURL else {
            state = .error("No server URL")
            return
        }

        state = .connecting
        logger.info("Voice: speech detected, requesting token")

        Task {
            do {
                let token = try await requestEphemeralToken(from: serverBaseURL)
                await openRealtimeWebSocket(token: token)
            } catch {
                state = .error("Token: \(error.localizedDescription)")
                logger.error("Voice: token request failed: \(error.localizedDescription)")
            }
        }
    }

    private func requestEphemeralToken(from baseURL: URL) async throws -> String {
        let url = baseURL.appendingPathComponent("api/realtime/token")
        var request = URLRequest(url: url)
        request.httpMethod = "POST"
        request.setValue("application/json", forHTTPHeaderField: "Content-Type")

        let (data, response) = try await URLSession.shared.data(for: request)
        guard let httpResp = response as? HTTPURLResponse,
              httpResp.statusCode == 200 else {
            throw VoiceError.tokenRequestFailed
        }

        guard let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let clientSecret = json["client_secret"] as? [String: Any],
              let token = clientSecret["value"] as? String else {
            throw VoiceError.tokenParseFailed
        }

        return token
    }

    private func openRealtimeWebSocket(token: String) async {
        let url = URL(string: "wss://api.openai.com/v1/realtime?model=gpt-4o-transcribe")!
        var request = URLRequest(url: url)
        request.setValue("Bearer \(token)", forHTTPHeaderField: "Authorization")
        request.setValue("realtime=v1", forHTTPHeaderField: "OpenAI-Beta")

        let session = URLSession(configuration: .default)
        let ws = session.webSocketTask(with: request)
        webSocket = ws
        ws.resume()

        // Configure the session for transcription with semantic VAD.
        let config: [String: Any] = [
            "type": "session.update",
            "session": [
                "input_audio_transcription": [
                    "model": "gpt-4o-transcribe"
                ],
                "turn_detection": [
                    "type": "semantic_vad",
                    "eagerness": "medium"
                ],
                "input_audio_noise_reduction": [
                    "type": "near_field"
                ]
            ]
        ]
        if let data = try? JSONSerialization.data(withJSONObject: config),
           let string = String(data: data, encoding: .utf8) {
            ws.send(.string(string)) { _ in }
        }

        state = .streaming
        resetSilenceTimer()
        logger.info("Voice: connected to OpenAI Realtime")

        // Start receive loop.
        Task { await receiveLoop() }
    }

    private func receiveLoop() async {
        guard let ws = webSocket else { return }

        while !Task.isCancelled {
            let message: URLSessionWebSocketTask.Message
            do {
                message = try await ws.receive()
            } catch {
                if !Task.isCancelled {
                    logger.debug("Voice: WebSocket receive ended: \(error.localizedDescription)")
                    await MainActor.run { closeOpenAI() }
                }
                return
            }

            guard case .string(let text) = message,
                  let data = text.data(using: .utf8),
                  let json = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
                  let type = json["type"] as? String else {
                continue
            }

            await MainActor.run {
                handleRealtimeEvent(type: type, json: json)
            }
        }
    }

    private func handleRealtimeEvent(type: String, json: [String: Any]) {
        switch type {
        case "conversation.item.input_audio_transcription.delta":
            if let delta = json["delta"] as? String {
                partialText += delta
            }

        case "conversation.item.input_audio_transcription.completed":
            if let transcript = json["transcript"] as? String, !transcript.isEmpty {
                logger.info("Voice: utterance complete: \(transcript.prefix(50))")
                onUtterance?(transcript)
            }
            partialText = ""

        case "input_audio_buffer.speech_started":
            isSpeaking = true
            resetSilenceTimer()

        case "input_audio_buffer.speech_stopped":
            isSpeaking = false
            resetSilenceTimer()

        case "error":
            if let error = json["error"] as? [String: Any],
               let msg = error["message"] as? String {
                logger.error("Voice: OpenAI error: \(msg)")
                state = .error(msg)
            }

        default:
            break
        }
    }

    // MARK: - Audio streaming

    private func sendAudioToOpenAI(_ buffer: AVAudioPCMBuffer) {
        guard let ws = webSocket else { return }

        // Convert to 24kHz mono PCM16.
        guard let pcmData = convertTo24kHzPCM16(buffer) else { return }
        let base64 = pcmData.base64EncodedString()

        let msg = """
        {"type":"input_audio_buffer.append","audio":"\(base64)"}
        """
        ws.send(.string(msg)) { error in
            if let error {
                logger.debug("Voice: send failed: \(error.localizedDescription)")
            }
        }
    }

    private func convertTo24kHzPCM16(_ buffer: AVAudioPCMBuffer) -> Data? {
        guard let channelData = buffer.floatChannelData else { return nil }
        let srcRate = buffer.format.sampleRate
        let srcCount = Int(buffer.frameLength)
        guard srcCount > 0, srcRate > 0 else { return nil }

        let dstRate = 24000.0
        let ratio = srcRate / dstRate
        let dstCount = Int(Double(srcCount) / ratio)
        guard dstCount > 0 else { return nil }

        var data = Data(count: dstCount * 2) // 16-bit = 2 bytes per sample
        data.withUnsafeMutableBytes { raw in
            let ptr = raw.bindMemory(to: Int16.self)
            for i in 0..<dstCount {
                let srcIdx = min(Int(Double(i) * ratio), srcCount - 1)
                let sample = max(-1.0, min(1.0, channelData[0][srcIdx]))
                ptr[i] = Int16(sample * 32767)
            }
        }
        return data
    }

    // MARK: - Silence management

    private func resetSilenceTimer() {
        silenceTimer?.cancel()
        silenceTimer = Task { [weak self] in
            try? await Task.sleep(for: .seconds(self?.silenceTimeout ?? 3.0))
            guard !Task.isCancelled else { return }
            await MainActor.run {
                self?.handleSilenceTimeout()
            }
        }
    }

    private func handleSilenceTimeout() {
        guard case .streaming = state else { return }
        logger.info("Voice: silence timeout, closing OpenAI connection")
        closeOpenAI()
        state = .listening // Back to local VAD
    }

    private func closeOpenAI() {
        silenceTimer?.cancel()
        silenceTimer = nil
        webSocket?.cancel(with: .goingAway, reason: nil)
        webSocket = nil
        partialText = ""
        isSpeaking = false
    }

    // MARK: - Errors

    enum VoiceError: LocalizedError {
        case tokenRequestFailed
        case tokenParseFailed

        var errorDescription: String? {
            switch self {
            case .tokenRequestFailed: "Failed to get ephemeral token"
            case .tokenParseFailed: "Failed to parse token response"
            }
        }
    }
}
