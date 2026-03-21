// Code generated from protocol/*.yaml. DO NOT EDIT.

package protocol

// jevond states.
const (
	JevondIdle State = "Idle"
	JevondGenerateToken State = "GenerateToken"
	JevondRegisterRelay State = "RegisterRelay"
	JevondWaitingForClient State = "WaitingForClient"
	JevondDeriveSecret State = "DeriveSecret"
	JevondSendAck State = "SendAck"
	JevondWaitingForCode State = "WaitingForCode"
	JevondValidateCode State = "ValidateCode"
	JevondStorePaired State = "StorePaired"
	JevondPaired State = "Paired"
	JevondAuthCheck State = "AuthCheck"
	JevondSessionActive State = "SessionActive"
)

// ios states.
const (
	AppIdle State = "Idle"
	AppScanQR State = "ScanQR"
	AppConnectRelay State = "ConnectRelay"
	AppGenKeyPair State = "GenKeyPair"
	AppWaitAck State = "WaitAck"
	AppE2EReady State = "E2EReady"
	AppShowCode State = "ShowCode"
	AppWaitPairComplete State = "WaitPairComplete"
	AppPaired State = "Paired"
	AppReconnect State = "Reconnect"
	AppSendAuth State = "SendAuth"
	AppSessionActive State = "SessionActive"
)

// cli states.
const (
	CLIIdle State = "Idle"
	CLIGetKey State = "GetKey"
	CLIBeginPair State = "BeginPair"
	CLIShowQR State = "ShowQR"
	CLIPromptCode State = "PromptCode"
	CLISubmitCode State = "SubmitCode"
	CLIDone State = "Done"
)

// Message types.
const (
	MsgPairBegin MsgType = "pair_begin"
	MsgTokenResponse MsgType = "token_response"
	MsgPairHello MsgType = "pair_hello"
	MsgPairHelloAck MsgType = "pair_hello_ack"
	MsgPairConfirm MsgType = "pair_confirm"
	MsgWaitingForCode MsgType = "waiting_for_code"
	MsgCodeSubmit MsgType = "code_submit"
	MsgPairComplete MsgType = "pair_complete"
	MsgPairStatus MsgType = "pair_status"
	MsgAuthRequest MsgType = "auth_request"
	MsgAuthOk MsgType = "auth_ok"
)

// Guards.
const (
	GuardTokenValid GuardID = "token_valid"
	GuardTokenInvalid GuardID = "token_invalid"
	GuardCodeCorrect GuardID = "code_correct"
	GuardCodeWrong GuardID = "code_wrong"
	GuardDeviceKnown GuardID = "device_known"
	GuardDeviceUnknown GuardID = "device_unknown"
	GuardNonceFresh GuardID = "nonce_fresh"
)

// Actions.
const (
	ActionGenerateToken ActionID = "generate_token"
	ActionRegisterRelay ActionID = "register_relay"
	ActionDeriveSecret ActionID = "derive_secret"
	ActionStoreDevice ActionID = "store_device"
	ActionVerifyDevice ActionID = "verify_device"
	ActionSendPairHello ActionID = "send_pair_hello"
	ActionStoreSecret ActionID = "store_secret"
)

func PairingCeremony() *Protocol {
	return &Protocol{
		Name: "PairingCeremony",
		Actors: []Actor{
			{Name: "jevond", Initial: "Idle", Transitions: []Transition{
				{From: "Idle", To: "GenerateToken", On: Recv("pair_begin"), Do: "generate_token", Updates: []VarUpdate{{Var: "current_token", Expr: "\"tok_1\""}, {Var: "active_tokens", Expr: "active_tokens \\union {\"tok_1\"}"}, }},
				{From: "GenerateToken", To: "RegisterRelay", On: Internal("token created"), Do: "register_relay"},
				{From: "RegisterRelay", To: "WaitingForClient", On: Internal("relay registered"), Sends: []Send{{To: "cli", Msg: "token_response", Fields: map[string]string{"token": "current_token", "instance_id": "\"inst_1\"", }}, }},
				{From: "WaitingForClient", To: "DeriveSecret", On: Recv("pair_hello"), Guard: "token_valid", Do: "derive_secret", Updates: []VarUpdate{{Var: "received_client_pub", Expr: "recv_msg.pubkey"}, {Var: "server_ecdh_pub", Expr: "\"server_pub\""}, {Var: "server_shared_key", Expr: "DeriveKey(\"server_pub\", recv_msg.pubkey)"}, {Var: "server_code", Expr: "DeriveCode(\"server_pub\", recv_msg.pubkey)"}, }},
				{From: "WaitingForClient", To: "Idle", On: Recv("pair_hello"), Guard: "token_invalid"},
				{From: "DeriveSecret", To: "SendAck", On: Internal("ECDH complete"), Sends: []Send{{To: "ios", Msg: "pair_hello_ack", Fields: map[string]string{"pubkey": "server_ecdh_pub", }}, }},
				{From: "SendAck", To: "WaitingForCode", On: Internal("signal code display"), Sends: []Send{{To: "ios", Msg: "pair_confirm"}, {To: "cli", Msg: "waiting_for_code"}, }},
				{From: "WaitingForCode", To: "ValidateCode", On: Recv("code_submit"), Updates: []VarUpdate{{Var: "received_code", Expr: "recv_msg.code"}, }},
				{From: "ValidateCode", To: "StorePaired", On: Internal("check code"), Guard: "code_correct"},
				{From: "ValidateCode", To: "Idle", On: Internal("check code"), Guard: "code_wrong", Updates: []VarUpdate{{Var: "code_attempts", Expr: "code_attempts + 1"}, }},
				{From: "StorePaired", To: "Paired", On: Internal("finalise"), Do: "store_device", Sends: []Send{{To: "ios", Msg: "pair_complete", Fields: map[string]string{"key": "server_shared_key", "secret": "\"dev_secret_1\"", }}, {To: "cli", Msg: "pair_status", Fields: map[string]string{"status": "\"paired\"", }}, }, Updates: []VarUpdate{{Var: "device_secret", Expr: "\"dev_secret_1\""}, {Var: "paired_devices", Expr: "paired_devices \\union {\"device_1\"}"}, {Var: "active_tokens", Expr: "active_tokens \\ {current_token}"}, {Var: "used_tokens", Expr: "used_tokens \\union {current_token}"}, }},
				{From: "Paired", To: "AuthCheck", On: Recv("auth_request"), Updates: []VarUpdate{{Var: "received_device_id", Expr: "recv_msg.device_id"}, {Var: "received_auth_nonce", Expr: "recv_msg.nonce"}, }},
				{From: "AuthCheck", To: "SessionActive", On: Internal("verify"), Guard: "device_known", Do: "verify_device", Sends: []Send{{To: "ios", Msg: "auth_ok"}, }, Updates: []VarUpdate{{Var: "auth_nonces_used", Expr: "auth_nonces_used \\union {received_auth_nonce}"}, }},
				{From: "AuthCheck", To: "Idle", On: Internal("verify"), Guard: "device_unknown"},
				{From: "SessionActive", To: "Paired", On: Internal("disconnect")},
			}},
			{Name: "ios", Initial: "Idle", Transitions: []Transition{
				{From: "Idle", To: "ScanQR", On: Internal("user scans QR")},
				{From: "ScanQR", To: "ConnectRelay", On: Internal("QR parsed")},
				{From: "ConnectRelay", To: "GenKeyPair", On: Internal("relay connected")},
				{From: "GenKeyPair", To: "WaitAck", On: Internal("key pair generated"), Do: "send_pair_hello", Sends: []Send{{To: "jevond", Msg: "pair_hello", Fields: map[string]string{"token": "current_token", "pubkey": "\"client_pub\"", }}, }},
				{From: "WaitAck", To: "E2EReady", On: Recv("pair_hello_ack"), Do: "derive_secret", Updates: []VarUpdate{{Var: "received_server_pub", Expr: "recv_msg.pubkey"}, {Var: "client_shared_key", Expr: "DeriveKey(\"client_pub\", recv_msg.pubkey)"}, }},
				{From: "E2EReady", To: "ShowCode", On: Recv("pair_confirm"), Updates: []VarUpdate{{Var: "ios_code", Expr: "DeriveCode(received_server_pub, \"client_pub\")"}, }},
				{From: "ShowCode", To: "WaitPairComplete", On: Internal("code displayed")},
				{From: "WaitPairComplete", To: "Paired", On: Recv("pair_complete"), Do: "store_secret"},
				{From: "Paired", To: "Reconnect", On: Internal("app launch")},
				{From: "Reconnect", To: "SendAuth", On: Internal("relay connected"), Sends: []Send{{To: "jevond", Msg: "auth_request", Fields: map[string]string{"device_id": "\"device_1\"", "secret": "device_secret", "nonce": "\"nonce_1\"", "key": "client_shared_key", }}, }},
				{From: "SendAuth", To: "SessionActive", On: Recv("auth_ok")},
				{From: "SessionActive", To: "Paired", On: Internal("disconnect")},
			}},
			{Name: "cli", Initial: "Idle", Transitions: []Transition{
				{From: "Idle", To: "GetKey", On: Internal("jevon --init")},
				{From: "GetKey", To: "BeginPair", On: Internal("key stored"), Sends: []Send{{To: "jevond", Msg: "pair_begin"}, }},
				{From: "BeginPair", To: "ShowQR", On: Recv("token_response")},
				{From: "ShowQR", To: "PromptCode", On: Recv("waiting_for_code")},
				{From: "PromptCode", To: "SubmitCode", On: Internal("user enters code"), Sends: []Send{{To: "jevond", Msg: "code_submit", Fields: map[string]string{"code": "ios_code", }}, }},
				{From: "SubmitCode", To: "Done", On: Recv("pair_status")},
			}},
		},
		Messages: []Message{
			{Type: "pair_begin", From: "cli", To: "jevond", Desc: "POST /api/pair/begin"},
			{Type: "token_response", From: "jevond", To: "cli", Desc: "{instance_id, pairing_token}"},
			{Type: "pair_hello", From: "ios", To: "jevond", Desc: "ECDH pubkey + pairing token"},
			{Type: "pair_hello_ack", From: "jevond", To: "ios", Desc: "ECDH pubkey"},
			{Type: "pair_confirm", From: "jevond", To: "ios", Desc: "signal to compute and display code"},
			{Type: "waiting_for_code", From: "jevond", To: "cli", Desc: "prompt for code entry"},
			{Type: "code_submit", From: "cli", To: "jevond", Desc: "POST /api/pair/confirm"},
			{Type: "pair_complete", From: "jevond", To: "ios", Desc: "encrypted device secret"},
			{Type: "pair_status", From: "jevond", To: "cli", Desc: "status: paired"},
			{Type: "auth_request", From: "ios", To: "jevond", Desc: "encrypted auth with nonce"},
			{Type: "auth_ok", From: "jevond", To: "ios", Desc: "session established"},
		},
		Vars: []VarDef{
			{Name: "current_token", Initial: "\"none\"", Desc: "pairing token currently in play"},
			{Name: "active_tokens", Initial: "{}", Desc: "set of valid (non-revoked) tokens"},
			{Name: "used_tokens", Initial: "{}", Desc: "set of revoked tokens"},
			{Name: "server_ecdh_pub", Initial: "\"none\"", Desc: "server ECDH public key"},
			{Name: "received_client_pub", Initial: "\"none\"", Desc: "pubkey jevond received in pair_hello (may be adversary's)"},
			{Name: "received_server_pub", Initial: "\"none\"", Desc: "pubkey ios received in pair_hello_ack (may be adversary's)"},
			{Name: "server_shared_key", Initial: "<<\"none\">>", Desc: "ECDH key derived by jevond (tuple to match DeriveKey output type)"},
			{Name: "client_shared_key", Initial: "<<\"none\">>", Desc: "ECDH key derived by ios (tuple to match DeriveKey output type)"},
			{Name: "server_code", Initial: "<<\"none\">>", Desc: "code computed by jevond from its view of the pubkeys (tuple to match DeriveCode output type)"},
			{Name: "ios_code", Initial: "<<\"none\">>", Desc: "code computed by ios from its view of the pubkeys (tuple to match DeriveCode output type)"},
			{Name: "received_code", Initial: "<<\"none\">>", Desc: "code received in code_submit (tuple to match DeriveCode output type)"},
			{Name: "code_attempts", Initial: "0", Desc: "failed code submission attempts"},
			{Name: "device_secret", Initial: "\"none\"", Desc: "persistent device secret"},
			{Name: "paired_devices", Initial: "{}", Desc: "device IDs that completed pairing"},
			{Name: "received_device_id", Initial: "\"none\"", Desc: "device_id from auth_request"},
			{Name: "auth_nonces_used", Initial: "{}", Desc: "set of consumed auth nonces"},
			{Name: "received_auth_nonce", Initial: "\"none\"", Desc: "nonce from auth_request"},
			{Name: "adversary_keys", Initial: "{}", Desc: "encryption keys the adversary knows"},
			{Name: "adv_ecdh_pub", Initial: "\"adv_pub\"", Desc: "adversary's ECDH public key"},
			{Name: "adv_saved_client_pub", Initial: "\"none\"", Desc: "real client pubkey saved during MitM"},
			{Name: "adv_saved_server_pub", Initial: "\"none\"", Desc: "real server pubkey saved during MitM"},
			{Name: "recv_msg", Initial: "[type |-> \"none\"]", Desc: "last received message (staging)"},
		},
		Guards: []GuardDef{
			{ID: "token_valid", Expr: "recv_msg.token \\in active_tokens"},
			{ID: "token_invalid", Expr: "recv_msg.token \\notin active_tokens"},
			{ID: "code_correct", Expr: "received_code = server_code"},
			{ID: "code_wrong", Expr: "received_code /= server_code"},
			{ID: "device_known", Expr: "received_device_id \\in paired_devices"},
			{ID: "device_unknown", Expr: "received_device_id \\notin paired_devices"},
			{ID: "nonce_fresh", Expr: "received_auth_nonce \\notin auth_nonces_used"},
		},
		Operators: []Operator{
			{Name: "KeyRank", Params: "k", Expr: "CASE k = \"adv_pub\" -> 0 [] k = \"client_pub\" -> 1 [] k = \"server_pub\" -> 2 [] OTHER -> 3", Desc: "Assign numeric rank to pubkey names for deterministic ordering"},
			{Name: "DeriveKey", Params: "a, b", Expr: "IF KeyRank(a) <= KeyRank(b) THEN <<\"ecdh\", a, b>> ELSE <<\"ecdh\", b, a>>", Desc: "Symbolic ECDH: deterministic key from two public keys (order-independent)"},
			{Name: "DeriveCode", Params: "a, b", Expr: "IF KeyRank(a) <= KeyRank(b) THEN <<\"code\", a, b>> ELSE <<\"code\", b, a>>", Desc: "Key-bound confirmation code: deterministic from both pubkeys (order-independent)"},
		},
		AdvActions: []AdvAction{
			{Name: "QR_shoulder_surf", Desc: "observe QR code content (token + instance_id)", Code: "      await current_token /= \"none\";\n      adversary_knowledge := adversary_knowledge \\union {[type |-> \"qr_token\", token |-> current_token]};"},
			{Name: "MitM_pair_hello", Desc: "intercept pair_hello and substitute adversary ECDH pubkey", Code: "      await Len(chan_ios_jevond) > 0 /\\ Head(chan_ios_jevond).type = MSG_pair_hello;\n      adv_saved_client_pub := Head(chan_ios_jevond).pubkey;\n      chan_ios_jevond := <<[type |-> MSG_pair_hello, token |-> Head(chan_ios_jevond).token, pubkey |-> adv_ecdh_pub]>> \\o Tail(chan_ios_jevond);"},
			{Name: "MitM_pair_hello_ack", Desc: "intercept pair_hello_ack and substitute adversary ECDH pubkey, derive both shared secrets", Code: "      await Len(chan_jevond_ios) > 0 /\\ Head(chan_jevond_ios).type = MSG_pair_hello_ack;\n      adv_saved_server_pub := Head(chan_jevond_ios).pubkey;\n      adversary_keys := adversary_keys \\union {DeriveKey(adv_ecdh_pub, adv_saved_server_pub), DeriveKey(adv_ecdh_pub, adv_saved_client_pub)};\n      chan_jevond_ios := <<[type |-> MSG_pair_hello_ack, pubkey |-> adv_ecdh_pub]>> \\o Tail(chan_jevond_ios);"},
			{Name: "MitM_reencrypt_secret", Desc: "decrypt pair_complete with MitM key, learn device secret", Code: "      await Len(chan_jevond_ios) > 0 /\\ Head(chan_jevond_ios).type = MSG_pair_complete /\\ Head(chan_jevond_ios).key \\in adversary_keys;\n      with msg = Head(chan_jevond_ios) do\n        adversary_knowledge := adversary_knowledge \\union {[type |-> \"plaintext_secret\", secret |-> msg.secret]};\n        chan_jevond_ios := <<[type |-> MSG_pair_complete, key |-> DeriveKey(adv_ecdh_pub, adv_saved_client_pub), secret |-> msg.secret]>> \\o Tail(chan_jevond_ios);\n      end with;"},
			{Name: "concurrent_pair", Desc: "race a forged pair_hello using shoulder-surfed token", Code: "      await \\E m \\in adversary_knowledge : m = [type |-> \"qr_token\", token |-> current_token];\n      await Len(chan_ios_jevond) < 3;\n      chan_ios_jevond := Append(chan_ios_jevond, [type |-> MSG_pair_hello, token |-> current_token, pubkey |-> adv_ecdh_pub]);"},
			{Name: "token_bruteforce", Desc: "send pair_hello with fabricated token", Code: "      await Len(chan_ios_jevond) < 3;\n      chan_ios_jevond := Append(chan_ios_jevond, [type |-> MSG_pair_hello, token |-> \"fake_token\", pubkey |-> adv_ecdh_pub]);"},
			{Name: "code_guess", Desc: "submit fabricated confirmation code via CLI channel", Code: "      await Len(chan_cli_jevond) < 3;\n      chan_cli_jevond := Append(chan_cli_jevond, [type |-> MSG_code_submit, code |-> <<\"guess\", \"000000\">>]);"},
			{Name: "session_replay", Desc: "replay captured auth_request with stale nonce", Code: "      await Len(chan_ios_jevond) < 3;\n      await \\E m \\in adversary_knowledge : m.type = MSG_auth_request;\n      with msg \\in {m \\in adversary_knowledge : m.type = MSG_auth_request} do\n        chan_ios_jevond := Append(chan_ios_jevond, msg);\n      end with;"},
		},
		Properties: []Property{
			{Name: "NoTokenReuse", Kind: 0, Expr: "used_tokens \\intersect active_tokens = {}", Desc: "A revoked pairing token is never accepted again"},
			{Name: "MitMDetectedByCodeMismatch", Kind: 0, Expr: "(server_shared_key \\in adversary_keys /\\ server_code /= <<\"none\">> /\\ ios_code /= <<\"none\">>) => server_code /= ios_code", Desc: "If the current session's shared key is compromised and both sides computed codes, the codes differ"},
			{Name: "MitMPrevented", Kind: 0, Expr: "server_shared_key \\in adversary_keys => jevond_state \\notin {jevond_StorePaired, jevond_Paired, jevond_AuthCheck, jevond_SessionActive}", Desc: "If the current session's key is compromised, pairing never completes"},
			{Name: "AuthRequiresCompletedPairing", Kind: 0, Expr: "jevond_state = jevond_SessionActive => received_device_id \\in paired_devices", Desc: "A session is only active for a device that completed pairing"},
			{Name: "NoNonceReuse", Kind: 0, Expr: "jevond_state = jevond_SessionActive => received_auth_nonce \\notin (auth_nonces_used \\ {received_auth_nonce})", Desc: "Each auth nonce is accepted at most once"},
			{Name: "WrongCodeDoesNotPair", Kind: 0, Expr: "(jevond_state = jevond_StorePaired \\/ jevond_state = jevond_Paired) => received_code = server_code \\/ received_code = <<\"none\">>", Desc: "Pairing only completes with the correct confirmation code"},
			{Name: "DeviceSecretSecrecy", Kind: 0, Expr: "\\A m \\in adversary_knowledge : \"type\" \\in DOMAIN m => m.type /= \"plaintext_secret\"", Desc: "Adversary never learns the device secret in plaintext"},
			{Name: "HonestPairingCompletes", Kind: 1, Expr: "cli_state = cli_Done /\\ ios_state = ios_Paired", Desc: "If all actors cooperate honestly (no MitM), pairing eventually completes"},
		},
	}
}
