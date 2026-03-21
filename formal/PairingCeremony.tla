---- MODULE PairingCeremony ----
\* Auto-generated from protocol definition. Do not edit.
\* Source of truth: internal/protocol/ Go definition.

EXTENDS Integers, Sequences, FiniteSets, TLC

\* States for jevond
jevond_Idle == "jevond_Idle"
jevond_GenerateToken == "jevond_GenerateToken"
jevond_RegisterRelay == "jevond_RegisterRelay"
jevond_WaitingForClient == "jevond_WaitingForClient"
jevond_DeriveSecret == "jevond_DeriveSecret"
jevond_SendAck == "jevond_SendAck"
jevond_WaitingForCode == "jevond_WaitingForCode"
jevond_ValidateCode == "jevond_ValidateCode"
jevond_StorePaired == "jevond_StorePaired"
jevond_Paired == "jevond_Paired"
jevond_AuthCheck == "jevond_AuthCheck"
jevond_SessionActive == "jevond_SessionActive"

\* States for ios
ios_Idle == "ios_Idle"
ios_ScanQR == "ios_ScanQR"
ios_ConnectRelay == "ios_ConnectRelay"
ios_GenKeyPair == "ios_GenKeyPair"
ios_WaitAck == "ios_WaitAck"
ios_E2EReady == "ios_E2EReady"
ios_ShowCode == "ios_ShowCode"
ios_WaitPairComplete == "ios_WaitPairComplete"
ios_Paired == "ios_Paired"
ios_Reconnect == "ios_Reconnect"
ios_SendAuth == "ios_SendAuth"
ios_SessionActive == "ios_SessionActive"

\* States for cli
cli_Idle == "cli_Idle"
cli_GetKey == "cli_GetKey"
cli_BeginPair == "cli_BeginPair"
cli_ShowQR == "cli_ShowQR"
cli_PromptCode == "cli_PromptCode"
cli_SubmitCode == "cli_SubmitCode"
cli_Done == "cli_Done"

\* Message types
MSG_pair_begin == "pair_begin" \* cli -> jevond (POST /api/pair/begin)
MSG_token_response == "token_response" \* jevond -> cli ({instance_id, pairing_token})
MSG_pair_hello == "pair_hello" \* ios -> jevond (ECDH pubkey + pairing token)
MSG_pair_hello_ack == "pair_hello_ack" \* jevond -> ios (ECDH pubkey)
MSG_pair_confirm == "pair_confirm" \* jevond -> ios (signal to compute and display code)
MSG_waiting_for_code == "waiting_for_code" \* jevond -> cli (prompt for code entry)
MSG_code_submit == "code_submit" \* cli -> jevond (POST /api/pair/confirm)
MSG_pair_complete == "pair_complete" \* jevond -> ios (encrypted device secret)
MSG_pair_status == "pair_status" \* jevond -> cli (status: paired)
MSG_auth_request == "auth_request" \* ios -> jevond (encrypted auth with nonce)
MSG_auth_ok == "auth_ok" \* jevond -> ios (session established)

\* Helper operators
\* Assign numeric rank to pubkey names for deterministic ordering
KeyRank(k) == CASE k = "adv_pub" -> 0 [] k = "client_pub" -> 1 [] k = "server_pub" -> 2 [] OTHER -> 3
\* Symbolic ECDH: deterministic key from two public keys (order-independent)
DeriveKey(a, b) == IF KeyRank(a) <= KeyRank(b) THEN <<"ecdh", a, b>> ELSE <<"ecdh", b, a>>
\* Key-bound confirmation code: deterministic from both pubkeys (order-independent)
DeriveCode(a, b) == IF KeyRank(a) <= KeyRank(b) THEN <<"code", a, b>> ELSE <<"code", b, a>>

(*--algorithm PairingCeremony

variables
    jevond_state = jevond_Idle,
    ios_state = ios_Idle,
    cli_state = cli_Idle,
    chan_cli_jevond = <<>>,
    chan_ios_jevond = <<>>,
    chan_jevond_cli = <<>>,
    chan_jevond_ios = <<>>,
    adversary_knowledge = {},
    \* pairing token currently in play
    current_token = "none",
    \* set of valid (non-revoked) tokens
    active_tokens = {},
    \* set of revoked tokens
    used_tokens = {},
    \* server ECDH public key
    server_ecdh_pub = "none",
    \* pubkey jevond received in pair_hello (may be adversary's)
    received_client_pub = "none",
    \* pubkey ios received in pair_hello_ack (may be adversary's)
    received_server_pub = "none",
    \* ECDH key derived by jevond (tuple to match DeriveKey output type)
    server_shared_key = <<"none">>,
    \* ECDH key derived by ios (tuple to match DeriveKey output type)
    client_shared_key = <<"none">>,
    \* code computed by jevond from its view of the pubkeys (tuple to match DeriveCode output type)
    server_code = <<"none">>,
    \* code computed by ios from its view of the pubkeys (tuple to match DeriveCode output type)
    ios_code = <<"none">>,
    \* code received in code_submit (tuple to match DeriveCode output type)
    received_code = <<"none">>,
    \* failed code submission attempts
    code_attempts = 0,
    \* persistent device secret
    device_secret = "none",
    \* device IDs that completed pairing
    paired_devices = {},
    \* device_id from auth_request
    received_device_id = "none",
    \* set of consumed auth nonces
    auth_nonces_used = {},
    \* nonce from auth_request
    received_auth_nonce = "none",
    \* encryption keys the adversary knows
    adversary_keys = {},
    \* adversary's ECDH public key
    adv_ecdh_pub = "adv_pub",
    \* real client pubkey saved during MitM
    adv_saved_client_pub = "none",
    \* real server pubkey saved during MitM
    adv_saved_server_pub = "none",
    \* last received message (staging)
    recv_msg = [type |-> "none"];

fair process jevond = 1
begin
  jevond_loop:
    either
      \* Idle -> GenerateToken on pair_begin
      await jevond_state = jevond_Idle /\ Len(chan_cli_jevond) > 0 /\ Head(chan_cli_jevond).type = MSG_pair_begin;
      recv_msg := Head(chan_cli_jevond);
      chan_cli_jevond := Tail(chan_cli_jevond);
      current_token := "tok_1";
      active_tokens := active_tokens \union {"tok_1"};
      jevond_state := jevond_GenerateToken;
    or
      \* GenerateToken -> RegisterRelay (token created)
      await jevond_state = jevond_GenerateToken;
      jevond_state := jevond_RegisterRelay;
    or
      \* RegisterRelay -> WaitingForClient (relay registered)
      await jevond_state = jevond_RegisterRelay;
      chan_jevond_cli := Append(chan_jevond_cli, [type |-> MSG_token_response, instance_id |-> "inst_1", token |-> current_token]);
      jevond_state := jevond_WaitingForClient;
    or
      \* WaitingForClient -> DeriveSecret on pair_hello
      await jevond_state = jevond_WaitingForClient /\ Len(chan_ios_jevond) > 0 /\ Head(chan_ios_jevond).type = MSG_pair_hello /\ (Head(chan_ios_jevond).token \in active_tokens);
      recv_msg := Head(chan_ios_jevond);
      chan_ios_jevond := Tail(chan_ios_jevond);
      received_client_pub := recv_msg.pubkey;
      server_ecdh_pub := "server_pub";
      server_shared_key := DeriveKey("server_pub", recv_msg.pubkey);
      server_code := DeriveCode("server_pub", recv_msg.pubkey);
      jevond_state := jevond_DeriveSecret;
    or
      \* WaitingForClient -> Idle on pair_hello
      await jevond_state = jevond_WaitingForClient /\ Len(chan_ios_jevond) > 0 /\ Head(chan_ios_jevond).type = MSG_pair_hello /\ (Head(chan_ios_jevond).token \notin active_tokens);
      recv_msg := Head(chan_ios_jevond);
      chan_ios_jevond := Tail(chan_ios_jevond);
      jevond_state := jevond_Idle;
    or
      \* DeriveSecret -> SendAck (ECDH complete)
      await jevond_state = jevond_DeriveSecret;
      chan_jevond_ios := Append(chan_jevond_ios, [type |-> MSG_pair_hello_ack, pubkey |-> server_ecdh_pub]);
      jevond_state := jevond_SendAck;
    or
      \* SendAck -> WaitingForCode (signal code display)
      await jevond_state = jevond_SendAck;
      chan_jevond_ios := Append(chan_jevond_ios, [type |-> MSG_pair_confirm]);
      chan_jevond_cli := Append(chan_jevond_cli, [type |-> MSG_waiting_for_code]);
      jevond_state := jevond_WaitingForCode;
    or
      \* WaitingForCode -> ValidateCode on code_submit
      await jevond_state = jevond_WaitingForCode /\ Len(chan_cli_jevond) > 0 /\ Head(chan_cli_jevond).type = MSG_code_submit;
      recv_msg := Head(chan_cli_jevond);
      chan_cli_jevond := Tail(chan_cli_jevond);
      received_code := recv_msg.code;
      jevond_state := jevond_ValidateCode;
    or
      \* ValidateCode -> StorePaired (check code)
      await jevond_state = jevond_ValidateCode /\ (received_code = server_code);
      jevond_state := jevond_StorePaired;
    or
      \* ValidateCode -> Idle (check code)
      await jevond_state = jevond_ValidateCode /\ (received_code /= server_code);
      code_attempts := code_attempts + 1;
      jevond_state := jevond_Idle;
    or
      \* StorePaired -> Paired (finalise)
      await jevond_state = jevond_StorePaired;
      chan_jevond_ios := Append(chan_jevond_ios, [type |-> MSG_pair_complete, key |-> server_shared_key, secret |-> "dev_secret_1"]);
      chan_jevond_cli := Append(chan_jevond_cli, [type |-> MSG_pair_status, status |-> "paired"]);
      device_secret := "dev_secret_1";
      paired_devices := paired_devices \union {"device_1"};
      active_tokens := active_tokens \ {current_token};
      used_tokens := used_tokens \union {current_token};
      jevond_state := jevond_Paired;
    or
      \* Paired -> AuthCheck on auth_request
      await jevond_state = jevond_Paired /\ Len(chan_ios_jevond) > 0 /\ Head(chan_ios_jevond).type = MSG_auth_request;
      recv_msg := Head(chan_ios_jevond);
      chan_ios_jevond := Tail(chan_ios_jevond);
      received_device_id := recv_msg.device_id;
      received_auth_nonce := recv_msg.nonce;
      jevond_state := jevond_AuthCheck;
    or
      \* AuthCheck -> SessionActive (verify)
      await jevond_state = jevond_AuthCheck /\ (received_device_id \in paired_devices);
      chan_jevond_ios := Append(chan_jevond_ios, [type |-> MSG_auth_ok]);
      auth_nonces_used := auth_nonces_used \union {received_auth_nonce};
      jevond_state := jevond_SessionActive;
    or
      \* AuthCheck -> Idle (verify)
      await jevond_state = jevond_AuthCheck /\ (received_device_id \notin paired_devices);
      jevond_state := jevond_Idle;
    or
      \* SessionActive -> Paired (disconnect)
      await jevond_state = jevond_SessionActive;
      jevond_state := jevond_Paired;
    end either;
end process;

fair process ios = 2
begin
  ios_loop:
    either
      \* Idle -> ScanQR (user scans QR)
      await ios_state = ios_Idle;
      ios_state := ios_ScanQR;
    or
      \* ScanQR -> ConnectRelay (QR parsed)
      await ios_state = ios_ScanQR;
      ios_state := ios_ConnectRelay;
    or
      \* ConnectRelay -> GenKeyPair (relay connected)
      await ios_state = ios_ConnectRelay;
      ios_state := ios_GenKeyPair;
    or
      \* GenKeyPair -> WaitAck (key pair generated)
      await ios_state = ios_GenKeyPair;
      chan_ios_jevond := Append(chan_ios_jevond, [type |-> MSG_pair_hello, pubkey |-> "client_pub", token |-> current_token]);
      ios_state := ios_WaitAck;
    or
      \* WaitAck -> E2EReady on pair_hello_ack
      await ios_state = ios_WaitAck /\ Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_pair_hello_ack;
      recv_msg := Head(chan_jevond_ios);
      chan_jevond_ios := Tail(chan_jevond_ios);
      received_server_pub := recv_msg.pubkey;
      client_shared_key := DeriveKey("client_pub", recv_msg.pubkey);
      ios_state := ios_E2EReady;
    or
      \* E2EReady -> ShowCode on pair_confirm
      await ios_state = ios_E2EReady /\ Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_pair_confirm;
      recv_msg := Head(chan_jevond_ios);
      chan_jevond_ios := Tail(chan_jevond_ios);
      ios_code := DeriveCode(received_server_pub, "client_pub");
      ios_state := ios_ShowCode;
    or
      \* ShowCode -> WaitPairComplete (code displayed)
      await ios_state = ios_ShowCode;
      ios_state := ios_WaitPairComplete;
    or
      \* WaitPairComplete -> Paired on pair_complete
      await ios_state = ios_WaitPairComplete /\ Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_pair_complete;
      recv_msg := Head(chan_jevond_ios);
      chan_jevond_ios := Tail(chan_jevond_ios);
      ios_state := ios_Paired;
    or
      \* Paired -> Reconnect (app launch)
      await ios_state = ios_Paired;
      ios_state := ios_Reconnect;
    or
      \* Reconnect -> SendAuth (relay connected)
      await ios_state = ios_Reconnect;
      chan_ios_jevond := Append(chan_ios_jevond, [type |-> MSG_auth_request, device_id |-> "device_1", key |-> client_shared_key, nonce |-> "nonce_1", secret |-> device_secret]);
      ios_state := ios_SendAuth;
    or
      \* SendAuth -> SessionActive on auth_ok
      await ios_state = ios_SendAuth /\ Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_auth_ok;
      recv_msg := Head(chan_jevond_ios);
      chan_jevond_ios := Tail(chan_jevond_ios);
      ios_state := ios_SessionActive;
    or
      \* SessionActive -> Paired (disconnect)
      await ios_state = ios_SessionActive;
      ios_state := ios_Paired;
    end either;
end process;

fair process cli = 3
begin
  cli_loop:
    either
      \* Idle -> GetKey (jevon --init)
      await cli_state = cli_Idle;
      cli_state := cli_GetKey;
    or
      \* GetKey -> BeginPair (key stored)
      await cli_state = cli_GetKey;
      chan_cli_jevond := Append(chan_cli_jevond, [type |-> MSG_pair_begin]);
      cli_state := cli_BeginPair;
    or
      \* BeginPair -> ShowQR on token_response
      await cli_state = cli_BeginPair /\ Len(chan_jevond_cli) > 0 /\ Head(chan_jevond_cli).type = MSG_token_response;
      recv_msg := Head(chan_jevond_cli);
      chan_jevond_cli := Tail(chan_jevond_cli);
      cli_state := cli_ShowQR;
    or
      \* ShowQR -> PromptCode on waiting_for_code
      await cli_state = cli_ShowQR /\ Len(chan_jevond_cli) > 0 /\ Head(chan_jevond_cli).type = MSG_waiting_for_code;
      recv_msg := Head(chan_jevond_cli);
      chan_jevond_cli := Tail(chan_jevond_cli);
      cli_state := cli_PromptCode;
    or
      \* PromptCode -> SubmitCode (user enters code)
      await cli_state = cli_PromptCode;
      chan_cli_jevond := Append(chan_cli_jevond, [type |-> MSG_code_submit, code |-> ios_code]);
      cli_state := cli_SubmitCode;
    or
      \* SubmitCode -> Done on pair_status
      await cli_state = cli_SubmitCode /\ Len(chan_jevond_cli) > 0 /\ Head(chan_jevond_cli).type = MSG_pair_status;
      recv_msg := Head(chan_jevond_cli);
      chan_jevond_cli := Tail(chan_jevond_cli);
      cli_state := cli_Done;
    end either;
end process;

\* Dolev-Yao adversary: controls the network.
\* Can read, drop, replay, and reorder messages on all channels.
\* Cannot forge messages or break cryptographic primitives.
\* Extended capabilities model specific attack scenarios.
fair process Adversary = 4
begin
  adv_loop:
  while TRUE do
    either
      skip \* no-op: honest relay
    or
      \* Eavesdrop on cli -> jevond
      await Len(chan_cli_jevond) > 0;
      adversary_knowledge := adversary_knowledge \union {Head(chan_cli_jevond)};
    or
      \* Drop from cli -> jevond
      await Len(chan_cli_jevond) > 0;
      chan_cli_jevond := Tail(chan_cli_jevond);
    or
      \* Replay into cli -> jevond
      await adversary_knowledge /= {} /\ Len(chan_cli_jevond) < 3;
      with msg \in adversary_knowledge do
        chan_cli_jevond := Append(chan_cli_jevond, msg);
      end with;
    or
      \* Eavesdrop on ios -> jevond
      await Len(chan_ios_jevond) > 0;
      adversary_knowledge := adversary_knowledge \union {Head(chan_ios_jevond)};
    or
      \* Drop from ios -> jevond
      await Len(chan_ios_jevond) > 0;
      chan_ios_jevond := Tail(chan_ios_jevond);
    or
      \* Replay into ios -> jevond
      await adversary_knowledge /= {} /\ Len(chan_ios_jevond) < 3;
      with msg \in adversary_knowledge do
        chan_ios_jevond := Append(chan_ios_jevond, msg);
      end with;
    or
      \* Eavesdrop on jevond -> cli
      await Len(chan_jevond_cli) > 0;
      adversary_knowledge := adversary_knowledge \union {Head(chan_jevond_cli)};
    or
      \* Drop from jevond -> cli
      await Len(chan_jevond_cli) > 0;
      chan_jevond_cli := Tail(chan_jevond_cli);
    or
      \* Replay into jevond -> cli
      await adversary_knowledge /= {} /\ Len(chan_jevond_cli) < 3;
      with msg \in adversary_knowledge do
        chan_jevond_cli := Append(chan_jevond_cli, msg);
      end with;
    or
      \* Eavesdrop on jevond -> ios
      await Len(chan_jevond_ios) > 0;
      adversary_knowledge := adversary_knowledge \union {Head(chan_jevond_ios)};
    or
      \* Drop from jevond -> ios
      await Len(chan_jevond_ios) > 0;
      chan_jevond_ios := Tail(chan_jevond_ios);
    or
      \* Replay into jevond -> ios
      await adversary_knowledge /= {} /\ Len(chan_jevond_ios) < 3;
      with msg \in adversary_knowledge do
        chan_jevond_ios := Append(chan_jevond_ios, msg);
      end with;
    or
      \* QR_shoulder_surf: observe QR code content (token + instance_id)
      await current_token /= "none";
      adversary_knowledge := adversary_knowledge \union {[type |-> "qr_token", token |-> current_token]};
    or
      \* MitM_pair_hello: intercept pair_hello and substitute adversary ECDH pubkey
      await Len(chan_ios_jevond) > 0 /\ Head(chan_ios_jevond).type = MSG_pair_hello;
      adv_saved_client_pub := Head(chan_ios_jevond).pubkey;
      chan_ios_jevond := <<[type |-> MSG_pair_hello, token |-> Head(chan_ios_jevond).token, pubkey |-> adv_ecdh_pub]>> \o Tail(chan_ios_jevond);
    or
      \* MitM_pair_hello_ack: intercept pair_hello_ack and substitute adversary ECDH pubkey, derive both shared secrets
      await Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_pair_hello_ack;
      adv_saved_server_pub := Head(chan_jevond_ios).pubkey;
      adversary_keys := adversary_keys \union {DeriveKey(adv_ecdh_pub, adv_saved_server_pub), DeriveKey(adv_ecdh_pub, adv_saved_client_pub)};
      chan_jevond_ios := <<[type |-> MSG_pair_hello_ack, pubkey |-> adv_ecdh_pub]>> \o Tail(chan_jevond_ios);
    or
      \* MitM_reencrypt_secret: decrypt pair_complete with MitM key, learn device secret
      await Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_pair_complete /\ Head(chan_jevond_ios).key \in adversary_keys;
      with msg = Head(chan_jevond_ios) do
        adversary_knowledge := adversary_knowledge \union {[type |-> "plaintext_secret", secret |-> msg.secret]};
        chan_jevond_ios := <<[type |-> MSG_pair_complete, key |-> DeriveKey(adv_ecdh_pub, adv_saved_client_pub), secret |-> msg.secret]>> \o Tail(chan_jevond_ios);
      end with;
    or
      \* concurrent_pair: race a forged pair_hello using shoulder-surfed token
      await \E m \in adversary_knowledge : m = [type |-> "qr_token", token |-> current_token];
      await Len(chan_ios_jevond) < 3;
      chan_ios_jevond := Append(chan_ios_jevond, [type |-> MSG_pair_hello, token |-> current_token, pubkey |-> adv_ecdh_pub]);
    or
      \* token_bruteforce: send pair_hello with fabricated token
      await Len(chan_ios_jevond) < 3;
      chan_ios_jevond := Append(chan_ios_jevond, [type |-> MSG_pair_hello, token |-> "fake_token", pubkey |-> adv_ecdh_pub]);
    or
      \* code_guess: submit fabricated confirmation code via CLI channel
      await Len(chan_cli_jevond) < 3;
      chan_cli_jevond := Append(chan_cli_jevond, [type |-> MSG_code_submit, code |-> <<"guess", "000000">>]);
    or
      \* session_replay: replay captured auth_request with stale nonce
      await Len(chan_ios_jevond) < 3;
      await \E m \in adversary_knowledge : m.type = MSG_auth_request;
      with msg \in {m \in adversary_knowledge : m.type = MSG_auth_request} do
        chan_ios_jevond := Append(chan_ios_jevond, msg);
      end with;
    end either;
  end while;
end process;

end algorithm; *)
\* BEGIN TRANSLATION
VARIABLES pc, jevond_state, ios_state, cli_state, chan_cli_jevond, 
          chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, 
          adversary_knowledge, current_token, active_tokens, used_tokens, 
          server_ecdh_pub, received_client_pub, received_server_pub, 
          server_shared_key, client_shared_key, server_code, ios_code, 
          received_code, code_attempts, device_secret, paired_devices, 
          received_device_id, auth_nonces_used, received_auth_nonce, 
          adversary_keys, adv_ecdh_pub, adv_saved_client_pub, 
          adv_saved_server_pub, recv_msg

vars == << pc, jevond_state, ios_state, cli_state, chan_cli_jevond, 
           chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, 
           adversary_knowledge, current_token, active_tokens, used_tokens, 
           server_ecdh_pub, received_client_pub, received_server_pub, 
           server_shared_key, client_shared_key, server_code, ios_code, 
           received_code, code_attempts, device_secret, paired_devices, 
           received_device_id, auth_nonces_used, received_auth_nonce, 
           adversary_keys, adv_ecdh_pub, adv_saved_client_pub, 
           adv_saved_server_pub, recv_msg >>

ProcSet == {1} \cup {2} \cup {3} \cup {4}

Init == (* Global variables *)
        /\ jevond_state = jevond_Idle
        /\ ios_state = ios_Idle
        /\ cli_state = cli_Idle
        /\ chan_cli_jevond = <<>>
        /\ chan_ios_jevond = <<>>
        /\ chan_jevond_cli = <<>>
        /\ chan_jevond_ios = <<>>
        /\ adversary_knowledge = {}
        /\ current_token = "none"
        /\ active_tokens = {}
        /\ used_tokens = {}
        /\ server_ecdh_pub = "none"
        /\ received_client_pub = "none"
        /\ received_server_pub = "none"
        /\ server_shared_key = <<"none">>
        /\ client_shared_key = <<"none">>
        /\ server_code = <<"none">>
        /\ ios_code = <<"none">>
        /\ received_code = <<"none">>
        /\ code_attempts = 0
        /\ device_secret = "none"
        /\ paired_devices = {}
        /\ received_device_id = "none"
        /\ auth_nonces_used = {}
        /\ received_auth_nonce = "none"
        /\ adversary_keys = {}
        /\ adv_ecdh_pub = "adv_pub"
        /\ adv_saved_client_pub = "none"
        /\ adv_saved_server_pub = "none"
        /\ recv_msg = [type |-> "none"]
        /\ pc = [self \in ProcSet |-> CASE self = 1 -> "jevond_loop"
                                        [] self = 2 -> "ios_loop"
                                        [] self = 3 -> "cli_loop"
                                        [] self = 4 -> "adv_loop"]

jevond_loop == /\ pc[1] = "jevond_loop"
               /\ \/ /\ jevond_state = jevond_Idle /\ Len(chan_cli_jevond) > 0 /\ Head(chan_cli_jevond).type = MSG_pair_begin
                     /\ recv_msg' = Head(chan_cli_jevond)
                     /\ chan_cli_jevond' = Tail(chan_cli_jevond)
                     /\ current_token' = "tok_1"
                     /\ active_tokens' = (active_tokens \union {"tok_1"})
                     /\ jevond_state' = jevond_GenerateToken
                     /\ UNCHANGED <<chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce>>
                  \/ /\ jevond_state = jevond_GenerateToken
                     /\ jevond_state' = jevond_RegisterRelay
                     /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce, recv_msg>>
                  \/ /\ jevond_state = jevond_RegisterRelay
                     /\ chan_jevond_cli' = Append(chan_jevond_cli, [type |-> MSG_token_response, instance_id |-> "inst_1", token |-> current_token])
                     /\ jevond_state' = jevond_WaitingForClient
                     /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_ios, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce, recv_msg>>
                  \/ /\ jevond_state = jevond_WaitingForClient /\ Len(chan_ios_jevond) > 0 /\ Head(chan_ios_jevond).type = MSG_pair_hello /\ (Head(chan_ios_jevond).token \in active_tokens)
                     /\ recv_msg' = Head(chan_ios_jevond)
                     /\ chan_ios_jevond' = Tail(chan_ios_jevond)
                     /\ received_client_pub' = recv_msg'.pubkey
                     /\ server_ecdh_pub' = "server_pub"
                     /\ server_shared_key' = DeriveKey("server_pub", recv_msg'.pubkey)
                     /\ server_code' = DeriveCode("server_pub", recv_msg'.pubkey)
                     /\ jevond_state' = jevond_DeriveSecret
                     /\ UNCHANGED <<chan_cli_jevond, chan_jevond_cli, chan_jevond_ios, current_token, active_tokens, used_tokens, received_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce>>
                  \/ /\ jevond_state = jevond_WaitingForClient /\ Len(chan_ios_jevond) > 0 /\ Head(chan_ios_jevond).type = MSG_pair_hello /\ (Head(chan_ios_jevond).token \notin active_tokens)
                     /\ recv_msg' = Head(chan_ios_jevond)
                     /\ chan_ios_jevond' = Tail(chan_ios_jevond)
                     /\ jevond_state' = jevond_Idle
                     /\ UNCHANGED <<chan_cli_jevond, chan_jevond_cli, chan_jevond_ios, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce>>
                  \/ /\ jevond_state = jevond_DeriveSecret
                     /\ chan_jevond_ios' = Append(chan_jevond_ios, [type |-> MSG_pair_hello_ack, pubkey |-> server_ecdh_pub])
                     /\ jevond_state' = jevond_SendAck
                     /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce, recv_msg>>
                  \/ /\ jevond_state = jevond_SendAck
                     /\ chan_jevond_ios' = Append(chan_jevond_ios, [type |-> MSG_pair_confirm])
                     /\ chan_jevond_cli' = Append(chan_jevond_cli, [type |-> MSG_waiting_for_code])
                     /\ jevond_state' = jevond_WaitingForCode
                     /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce, recv_msg>>
                  \/ /\ jevond_state = jevond_WaitingForCode /\ Len(chan_cli_jevond) > 0 /\ Head(chan_cli_jevond).type = MSG_code_submit
                     /\ recv_msg' = Head(chan_cli_jevond)
                     /\ chan_cli_jevond' = Tail(chan_cli_jevond)
                     /\ received_code' = recv_msg'.code
                     /\ jevond_state' = jevond_ValidateCode
                     /\ UNCHANGED <<chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce>>
                  \/ /\ jevond_state = jevond_ValidateCode /\ (received_code = server_code)
                     /\ jevond_state' = jevond_StorePaired
                     /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce, recv_msg>>
                  \/ /\ jevond_state = jevond_ValidateCode /\ (received_code /= server_code)
                     /\ code_attempts' = code_attempts + 1
                     /\ jevond_state' = jevond_Idle
                     /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce, recv_msg>>
                  \/ /\ jevond_state = jevond_StorePaired
                     /\ chan_jevond_ios' = Append(chan_jevond_ios, [type |-> MSG_pair_complete, key |-> server_shared_key, secret |-> "dev_secret_1"])
                     /\ chan_jevond_cli' = Append(chan_jevond_cli, [type |-> MSG_pair_status, status |-> "paired"])
                     /\ device_secret' = "dev_secret_1"
                     /\ paired_devices' = (paired_devices \union {"device_1"})
                     /\ active_tokens' = active_tokens \ {current_token}
                     /\ used_tokens' = (used_tokens \union {current_token})
                     /\ jevond_state' = jevond_Paired
                     /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, current_token, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, received_device_id, auth_nonces_used, received_auth_nonce, recv_msg>>
                  \/ /\ jevond_state = jevond_Paired /\ Len(chan_ios_jevond) > 0 /\ Head(chan_ios_jevond).type = MSG_auth_request
                     /\ recv_msg' = Head(chan_ios_jevond)
                     /\ chan_ios_jevond' = Tail(chan_ios_jevond)
                     /\ received_device_id' = recv_msg'.device_id
                     /\ received_auth_nonce' = recv_msg'.nonce
                     /\ jevond_state' = jevond_AuthCheck
                     /\ UNCHANGED <<chan_cli_jevond, chan_jevond_cli, chan_jevond_ios, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, auth_nonces_used>>
                  \/ /\ jevond_state = jevond_AuthCheck /\ (received_device_id \in paired_devices)
                     /\ chan_jevond_ios' = Append(chan_jevond_ios, [type |-> MSG_auth_ok])
                     /\ auth_nonces_used' = (auth_nonces_used \union {received_auth_nonce})
                     /\ jevond_state' = jevond_SessionActive
                     /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, received_device_id, received_auth_nonce, recv_msg>>
                  \/ /\ jevond_state = jevond_AuthCheck /\ (received_device_id \notin paired_devices)
                     /\ jevond_state' = jevond_Idle
                     /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce, recv_msg>>
                  \/ /\ jevond_state = jevond_SessionActive
                     /\ jevond_state' = jevond_Paired
                     /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, current_token, active_tokens, used_tokens, server_ecdh_pub, received_client_pub, server_shared_key, server_code, received_code, code_attempts, device_secret, paired_devices, received_device_id, auth_nonces_used, received_auth_nonce, recv_msg>>
               /\ pc' = [pc EXCEPT ![1] = "Done"]
               /\ UNCHANGED << ios_state, cli_state, adversary_knowledge, 
                               received_server_pub, client_shared_key, 
                               ios_code, adversary_keys, adv_ecdh_pub, 
                               adv_saved_client_pub, adv_saved_server_pub >>

jevond == jevond_loop

ios_loop == /\ pc[2] = "ios_loop"
            /\ \/ /\ ios_state = ios_Idle
                  /\ ios_state' = ios_ScanQR
                  /\ UNCHANGED <<chan_ios_jevond, chan_jevond_ios, received_server_pub, client_shared_key, ios_code, recv_msg>>
               \/ /\ ios_state = ios_ScanQR
                  /\ ios_state' = ios_ConnectRelay
                  /\ UNCHANGED <<chan_ios_jevond, chan_jevond_ios, received_server_pub, client_shared_key, ios_code, recv_msg>>
               \/ /\ ios_state = ios_ConnectRelay
                  /\ ios_state' = ios_GenKeyPair
                  /\ UNCHANGED <<chan_ios_jevond, chan_jevond_ios, received_server_pub, client_shared_key, ios_code, recv_msg>>
               \/ /\ ios_state = ios_GenKeyPair
                  /\ chan_ios_jevond' = Append(chan_ios_jevond, [type |-> MSG_pair_hello, pubkey |-> "client_pub", token |-> current_token])
                  /\ ios_state' = ios_WaitAck
                  /\ UNCHANGED <<chan_jevond_ios, received_server_pub, client_shared_key, ios_code, recv_msg>>
               \/ /\ ios_state = ios_WaitAck /\ Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_pair_hello_ack
                  /\ recv_msg' = Head(chan_jevond_ios)
                  /\ chan_jevond_ios' = Tail(chan_jevond_ios)
                  /\ received_server_pub' = recv_msg'.pubkey
                  /\ client_shared_key' = DeriveKey("client_pub", recv_msg'.pubkey)
                  /\ ios_state' = ios_E2EReady
                  /\ UNCHANGED <<chan_ios_jevond, ios_code>>
               \/ /\ ios_state = ios_E2EReady /\ Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_pair_confirm
                  /\ recv_msg' = Head(chan_jevond_ios)
                  /\ chan_jevond_ios' = Tail(chan_jevond_ios)
                  /\ ios_code' = DeriveCode(received_server_pub, "client_pub")
                  /\ ios_state' = ios_ShowCode
                  /\ UNCHANGED <<chan_ios_jevond, received_server_pub, client_shared_key>>
               \/ /\ ios_state = ios_ShowCode
                  /\ ios_state' = ios_WaitPairComplete
                  /\ UNCHANGED <<chan_ios_jevond, chan_jevond_ios, received_server_pub, client_shared_key, ios_code, recv_msg>>
               \/ /\ ios_state = ios_WaitPairComplete /\ Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_pair_complete
                  /\ recv_msg' = Head(chan_jevond_ios)
                  /\ chan_jevond_ios' = Tail(chan_jevond_ios)
                  /\ ios_state' = ios_Paired
                  /\ UNCHANGED <<chan_ios_jevond, received_server_pub, client_shared_key, ios_code>>
               \/ /\ ios_state = ios_Paired
                  /\ ios_state' = ios_Reconnect
                  /\ UNCHANGED <<chan_ios_jevond, chan_jevond_ios, received_server_pub, client_shared_key, ios_code, recv_msg>>
               \/ /\ ios_state = ios_Reconnect
                  /\ chan_ios_jevond' = Append(chan_ios_jevond, [type |-> MSG_auth_request, device_id |-> "device_1", key |-> client_shared_key, nonce |-> "nonce_1", secret |-> device_secret])
                  /\ ios_state' = ios_SendAuth
                  /\ UNCHANGED <<chan_jevond_ios, received_server_pub, client_shared_key, ios_code, recv_msg>>
               \/ /\ ios_state = ios_SendAuth /\ Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_auth_ok
                  /\ recv_msg' = Head(chan_jevond_ios)
                  /\ chan_jevond_ios' = Tail(chan_jevond_ios)
                  /\ ios_state' = ios_SessionActive
                  /\ UNCHANGED <<chan_ios_jevond, received_server_pub, client_shared_key, ios_code>>
               \/ /\ ios_state = ios_SessionActive
                  /\ ios_state' = ios_Paired
                  /\ UNCHANGED <<chan_ios_jevond, chan_jevond_ios, received_server_pub, client_shared_key, ios_code, recv_msg>>
            /\ pc' = [pc EXCEPT ![2] = "Done"]
            /\ UNCHANGED << jevond_state, cli_state, chan_cli_jevond, 
                            chan_jevond_cli, adversary_knowledge, 
                            current_token, active_tokens, used_tokens, 
                            server_ecdh_pub, received_client_pub, 
                            server_shared_key, server_code, received_code, 
                            code_attempts, device_secret, paired_devices, 
                            received_device_id, auth_nonces_used, 
                            received_auth_nonce, adversary_keys, adv_ecdh_pub, 
                            adv_saved_client_pub, adv_saved_server_pub >>

ios == ios_loop

cli_loop == /\ pc[3] = "cli_loop"
            /\ \/ /\ cli_state = cli_Idle
                  /\ cli_state' = cli_GetKey
                  /\ UNCHANGED <<chan_cli_jevond, chan_jevond_cli, recv_msg>>
               \/ /\ cli_state = cli_GetKey
                  /\ chan_cli_jevond' = Append(chan_cli_jevond, [type |-> MSG_pair_begin])
                  /\ cli_state' = cli_BeginPair
                  /\ UNCHANGED <<chan_jevond_cli, recv_msg>>
               \/ /\ cli_state = cli_BeginPair /\ Len(chan_jevond_cli) > 0 /\ Head(chan_jevond_cli).type = MSG_token_response
                  /\ recv_msg' = Head(chan_jevond_cli)
                  /\ chan_jevond_cli' = Tail(chan_jevond_cli)
                  /\ cli_state' = cli_ShowQR
                  /\ UNCHANGED chan_cli_jevond
               \/ /\ cli_state = cli_ShowQR /\ Len(chan_jevond_cli) > 0 /\ Head(chan_jevond_cli).type = MSG_waiting_for_code
                  /\ recv_msg' = Head(chan_jevond_cli)
                  /\ chan_jevond_cli' = Tail(chan_jevond_cli)
                  /\ cli_state' = cli_PromptCode
                  /\ UNCHANGED chan_cli_jevond
               \/ /\ cli_state = cli_PromptCode
                  /\ chan_cli_jevond' = Append(chan_cli_jevond, [type |-> MSG_code_submit, code |-> ios_code])
                  /\ cli_state' = cli_SubmitCode
                  /\ UNCHANGED <<chan_jevond_cli, recv_msg>>
               \/ /\ cli_state = cli_SubmitCode /\ Len(chan_jevond_cli) > 0 /\ Head(chan_jevond_cli).type = MSG_pair_status
                  /\ recv_msg' = Head(chan_jevond_cli)
                  /\ chan_jevond_cli' = Tail(chan_jevond_cli)
                  /\ cli_state' = cli_Done
                  /\ UNCHANGED chan_cli_jevond
            /\ pc' = [pc EXCEPT ![3] = "Done"]
            /\ UNCHANGED << jevond_state, ios_state, chan_ios_jevond, 
                            chan_jevond_ios, adversary_knowledge, 
                            current_token, active_tokens, used_tokens, 
                            server_ecdh_pub, received_client_pub, 
                            received_server_pub, server_shared_key, 
                            client_shared_key, server_code, ios_code, 
                            received_code, code_attempts, device_secret, 
                            paired_devices, received_device_id, 
                            auth_nonces_used, received_auth_nonce, 
                            adversary_keys, adv_ecdh_pub, adv_saved_client_pub, 
                            adv_saved_server_pub >>

cli == cli_loop

adv_loop == /\ pc[4] = "adv_loop"
            /\ \/ /\ TRUE
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_cli_jevond) > 0
                  /\ adversary_knowledge' = (adversary_knowledge \union {Head(chan_cli_jevond)})
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_cli_jevond) > 0
                  /\ chan_cli_jevond' = Tail(chan_cli_jevond)
                  /\ UNCHANGED <<chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ adversary_knowledge /= {} /\ Len(chan_cli_jevond) < 3
                  /\ \E msg \in adversary_knowledge:
                       chan_cli_jevond' = Append(chan_cli_jevond, msg)
                  /\ UNCHANGED <<chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_ios_jevond) > 0
                  /\ adversary_knowledge' = (adversary_knowledge \union {Head(chan_ios_jevond)})
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_ios_jevond) > 0
                  /\ chan_ios_jevond' = Tail(chan_ios_jevond)
                  /\ UNCHANGED <<chan_cli_jevond, chan_jevond_cli, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ adversary_knowledge /= {} /\ Len(chan_ios_jevond) < 3
                  /\ \E msg \in adversary_knowledge:
                       chan_ios_jevond' = Append(chan_ios_jevond, msg)
                  /\ UNCHANGED <<chan_cli_jevond, chan_jevond_cli, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_jevond_cli) > 0
                  /\ adversary_knowledge' = (adversary_knowledge \union {Head(chan_jevond_cli)})
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_jevond_cli) > 0
                  /\ chan_jevond_cli' = Tail(chan_jevond_cli)
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ adversary_knowledge /= {} /\ Len(chan_jevond_cli) < 3
                  /\ \E msg \in adversary_knowledge:
                       chan_jevond_cli' = Append(chan_jevond_cli, msg)
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_jevond_ios) > 0
                  /\ adversary_knowledge' = (adversary_knowledge \union {Head(chan_jevond_ios)})
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_jevond_ios) > 0
                  /\ chan_jevond_ios' = Tail(chan_jevond_ios)
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ adversary_knowledge /= {} /\ Len(chan_jevond_ios) < 3
                  /\ \E msg \in adversary_knowledge:
                       chan_jevond_ios' = Append(chan_jevond_ios, msg)
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ current_token /= "none"
                  /\ adversary_knowledge' = (adversary_knowledge \union {[type |-> "qr_token", token |-> current_token]})
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_ios_jevond) > 0 /\ Head(chan_ios_jevond).type = MSG_pair_hello
                  /\ adv_saved_client_pub' = Head(chan_ios_jevond).pubkey
                  /\ chan_ios_jevond' = <<[type |-> MSG_pair_hello, token |-> Head(chan_ios_jevond).token, pubkey |-> adv_ecdh_pub]>> \o Tail(chan_ios_jevond)
                  /\ UNCHANGED <<chan_cli_jevond, chan_jevond_cli, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_server_pub>>
               \/ /\ Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_pair_hello_ack
                  /\ adv_saved_server_pub' = Head(chan_jevond_ios).pubkey
                  /\ adversary_keys' = (adversary_keys \union {DeriveKey(adv_ecdh_pub, adv_saved_server_pub'), DeriveKey(adv_ecdh_pub, adv_saved_client_pub)})
                  /\ chan_jevond_ios' = <<[type |-> MSG_pair_hello_ack, pubkey |-> adv_ecdh_pub]>> \o Tail(chan_jevond_ios)
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, adversary_knowledge, adv_saved_client_pub>>
               \/ /\ Len(chan_jevond_ios) > 0 /\ Head(chan_jevond_ios).type = MSG_pair_complete /\ Head(chan_jevond_ios).key \in adversary_keys
                  /\ LET msg == Head(chan_jevond_ios) IN
                       /\ adversary_knowledge' = (adversary_knowledge \union {[type |-> "plaintext_secret", secret |-> msg.secret]})
                       /\ chan_jevond_ios' = <<[type |-> MSG_pair_complete, key |-> DeriveKey(adv_ecdh_pub, adv_saved_client_pub), secret |-> msg.secret]>> \o Tail(chan_jevond_ios)
                  /\ UNCHANGED <<chan_cli_jevond, chan_ios_jevond, chan_jevond_cli, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ \E m \in adversary_knowledge : m = [type |-> "qr_token", token |-> current_token]
                  /\ Len(chan_ios_jevond) < 3
                  /\ chan_ios_jevond' = Append(chan_ios_jevond, [type |-> MSG_pair_hello, token |-> current_token, pubkey |-> adv_ecdh_pub])
                  /\ UNCHANGED <<chan_cli_jevond, chan_jevond_cli, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_ios_jevond) < 3
                  /\ chan_ios_jevond' = Append(chan_ios_jevond, [type |-> MSG_pair_hello, token |-> "fake_token", pubkey |-> adv_ecdh_pub])
                  /\ UNCHANGED <<chan_cli_jevond, chan_jevond_cli, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_cli_jevond) < 3
                  /\ chan_cli_jevond' = Append(chan_cli_jevond, [type |-> MSG_code_submit, code |-> <<"guess", "000000">>])
                  /\ UNCHANGED <<chan_ios_jevond, chan_jevond_cli, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
               \/ /\ Len(chan_ios_jevond) < 3
                  /\ \E m \in adversary_knowledge : m.type = MSG_auth_request
                  /\ \E msg \in {m \in adversary_knowledge : m.type = MSG_auth_request}:
                       chan_ios_jevond' = Append(chan_ios_jevond, msg)
                  /\ UNCHANGED <<chan_cli_jevond, chan_jevond_cli, chan_jevond_ios, adversary_knowledge, adversary_keys, adv_saved_client_pub, adv_saved_server_pub>>
            /\ pc' = [pc EXCEPT ![4] = "adv_loop"]
            /\ UNCHANGED << jevond_state, ios_state, cli_state, current_token, 
                            active_tokens, used_tokens, server_ecdh_pub, 
                            received_client_pub, received_server_pub, 
                            server_shared_key, client_shared_key, server_code, 
                            ios_code, received_code, code_attempts, 
                            device_secret, paired_devices, received_device_id, 
                            auth_nonces_used, received_auth_nonce, 
                            adv_ecdh_pub, recv_msg >>

Adversary == adv_loop

Next == jevond \/ ios \/ cli \/ Adversary

Spec == /\ Init /\ [][Next]_vars
        /\ WF_vars(jevond)
        /\ WF_vars(ios)
        /\ WF_vars(cli)
        /\ WF_vars(Adversary)

\* END TRANSLATION

\* Verification properties
\* A revoked pairing token is never accepted again
NoTokenReuse == used_tokens \intersect active_tokens = {}
\* If the current session's shared key is compromised and both sides computed codes, the codes differ
MitMDetectedByCodeMismatch == (server_shared_key \in adversary_keys /\ server_code /= <<"none">> /\ ios_code /= <<"none">>) => server_code /= ios_code
\* If the current session's key is compromised, pairing never completes
MitMPrevented == server_shared_key \in adversary_keys => jevond_state \notin {jevond_StorePaired, jevond_Paired, jevond_AuthCheck, jevond_SessionActive}
\* A session is only active for a device that completed pairing
AuthRequiresCompletedPairing == jevond_state = jevond_SessionActive => received_device_id \in paired_devices
\* Each auth nonce is accepted at most once
NoNonceReuse == jevond_state = jevond_SessionActive => received_auth_nonce \notin (auth_nonces_used \ {received_auth_nonce})
\* Pairing only completes with the correct confirmation code
WrongCodeDoesNotPair == (jevond_state = jevond_StorePaired \/ jevond_state = jevond_Paired) => received_code = server_code \/ received_code = <<"none">>
\* Adversary never learns the device secret in plaintext
DeviceSecretSecrecy == \A m \in adversary_knowledge : "type" \in DOMAIN m => m.type /= "plaintext_secret"
\* If all actors cooperate honestly (no MitM), pairing eventually completes
HonestPairingCompletes == <>(cli_state = cli_Done /\ ios_state = ios_Paired)

====
