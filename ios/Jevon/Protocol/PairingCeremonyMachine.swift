// Auto-generated from protocol definition. Do not edit.
// Source of truth: protocol/*.yaml

import Foundation

enum MessageType: String, Sendable {
    case pairBegin = "pair_begin"
    case tokenResponse = "token_response"
    case pairHello = "pair_hello"
    case pairHelloAck = "pair_hello_ack"
    case pairConfirm = "pair_confirm"
    case waitingForCode = "waiting_for_code"
    case codeSubmit = "code_submit"
    case pairComplete = "pair_complete"
    case pairStatus = "pair_status"
    case authRequest = "auth_request"
    case authOk = "auth_ok"
}

enum JevondState: String, Sendable {
    case idle = "Idle"
    case generateToken = "GenerateToken"
    case registerRelay = "RegisterRelay"
    case waitingForClient = "WaitingForClient"
    case deriveSecret = "DeriveSecret"
    case sendAck = "SendAck"
    case waitingForCode = "WaitingForCode"
    case validateCode = "ValidateCode"
    case storePaired = "StorePaired"
    case paired = "Paired"
    case authCheck = "AuthCheck"
    case sessionActive = "SessionActive"
}

final class JevondMachine: @unchecked Sendable {
    private(set) var state: JevondState

    init() {
        self.state = .idle
    }

    /// Process a received message. Returns the new state, or nil if rejected.
    func handleMessage(_ msg: MessageType, guard check: (String) -> Bool = { _ in true }) -> JevondState? {
        switch (state, msg) {
        case (.idle, .pairBegin):
            state = .generateToken
            return state
        case (.waitingForClient, .pairHello) where check("token_valid"):
            state = .deriveSecret
            return state
        case (.waitingForClient, .pairHello) where check("token_invalid"):
            state = .idle
            return state
        case (.waitingForCode, .codeSubmit):
            state = .validateCode
            return state
        case (.paired, .authRequest):
            state = .authCheck
            return state
        default:
            return nil
        }
    }

    /// Attempt an internal transition. Returns the new state, or nil if none available.
    func step(guard check: (String) -> Bool = { _ in true }) -> JevondState? {
        switch state {
        case .generateToken:
            state = .registerRelay
            return state
        case .registerRelay:
            state = .waitingForClient
            return state
        case .deriveSecret:
            state = .sendAck
            return state
        case .sendAck:
            state = .waitingForCode
            return state
        case .validateCode:
            if check("code_correct") {
                state = .storePaired
                return state
            }
            if check("code_wrong") {
                state = .idle
                return state
            }
            return nil
        case .storePaired:
            state = .paired
            return state
        case .authCheck:
            if check("device_known") {
                state = .sessionActive
                return state
            }
            if check("device_unknown") {
                state = .idle
                return state
            }
            return nil
        case .sessionActive:
            state = .paired
            return state
        default:
            return nil
        }
    }
}

enum IosState: String, Sendable {
    case idle = "Idle"
    case scanQR = "ScanQR"
    case connectRelay = "ConnectRelay"
    case genKeyPair = "GenKeyPair"
    case waitAck = "WaitAck"
    case e2EReady = "E2EReady"
    case showCode = "ShowCode"
    case waitPairComplete = "WaitPairComplete"
    case paired = "Paired"
    case reconnect = "Reconnect"
    case sendAuth = "SendAuth"
    case sessionActive = "SessionActive"
}

final class IosMachine: @unchecked Sendable {
    private(set) var state: IosState

    init() {
        self.state = .idle
    }

    /// Process a received message. Returns the new state, or nil if rejected.
    func handleMessage(_ msg: MessageType, guard check: (String) -> Bool = { _ in true }) -> IosState? {
        switch (state, msg) {
        case (.waitAck, .pairHelloAck):
            state = .e2EReady
            return state
        case (.e2EReady, .pairConfirm):
            state = .showCode
            return state
        case (.waitPairComplete, .pairComplete):
            state = .paired
            return state
        case (.sendAuth, .authOk):
            state = .sessionActive
            return state
        default:
            return nil
        }
    }

    /// Attempt an internal transition. Returns the new state, or nil if none available.
    func step(guard check: (String) -> Bool = { _ in true }) -> IosState? {
        switch state {
        case .idle:
            state = .scanQR
            return state
        case .scanQR:
            state = .connectRelay
            return state
        case .connectRelay:
            state = .genKeyPair
            return state
        case .genKeyPair:
            state = .waitAck
            return state
        case .showCode:
            state = .waitPairComplete
            return state
        case .paired:
            state = .reconnect
            return state
        case .reconnect:
            state = .sendAuth
            return state
        case .sessionActive:
            state = .paired
            return state
        default:
            return nil
        }
    }
}

enum CliState: String, Sendable {
    case idle = "Idle"
    case getKey = "GetKey"
    case beginPair = "BeginPair"
    case showQR = "ShowQR"
    case promptCode = "PromptCode"
    case submitCode = "SubmitCode"
    case done = "Done"
}

final class CliMachine: @unchecked Sendable {
    private(set) var state: CliState

    init() {
        self.state = .idle
    }

    /// Process a received message. Returns the new state, or nil if rejected.
    func handleMessage(_ msg: MessageType, guard check: (String) -> Bool = { _ in true }) -> CliState? {
        switch (state, msg) {
        case (.beginPair, .tokenResponse):
            state = .showQR
            return state
        case (.showQR, .waitingForCode):
            state = .promptCode
            return state
        case (.submitCode, .pairStatus):
            state = .done
            return state
        default:
            return nil
        }
    }

    /// Attempt an internal transition. Returns the new state, or nil if none available.
    func step(guard check: (String) -> Bool = { _ in true }) -> CliState? {
        switch state {
        case .idle:
            state = .getKey
            return state
        case .getKey:
            state = .beginPair
            return state
        case .promptCode:
            state = .submitCode
            return state
        default:
            return nil
        }
    }
}

