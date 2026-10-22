// Explanations behind the (i) icons. One place, so the same term always reads
// the same everywhere in the panel.

export const GLOSSARY = {
  // connection
  gatewayName: "A unique label for this gateway. Only used in the panel and reports.",
  status: "Active: the gateway listens on its port and accepts binds. Disabled: the port is closed and bound sessions are unbound.",
  worker: "The psg-worker process (one per host) that runs this gateway's SMPP listener. Clients connect to that worker's IP.",
  ip: "The IP address of the worker host. Filled in automatically; SMPP clients connect to this address.",
  port: "TCP port this gateway listens on. It must be inside the worker's port range, not excluded, and not used by any other gateway on that worker. Stop the gateway before changing it.",
  systemId: "The login name (system_id) an SMPP client sends in its bind request. Up to 15 characters, no spaces.",
  password: "The bind password. SMPP 3.4 allows at most 8 characters. Stored encrypted; reveal it from the gateway page.",
  dlrRule: "Decides what happens to each submit_sm: accept it (with a message ID and, if requested, a DLR carrying a chosen status and error code) or reject it with an SMPP error code.",

  // DLR rules
  mode: "Fixed: every message gets the first entry. Random: each entry is equally likely. Weighted: each entry gets its share of traffic.",
  distribution: "Probabilistic: every message is an independent random pick, so the mix is close to the shares. Exact: every block of 10,000 messages matches the shares exactly, in shuffled order.",
  seed: "Optional number that makes the random picks repeatable: the same seed gives the same sequence of results every run. Leave empty for a different sequence each time.",
  entryName: "Your label for this result. Shown in previews and on the gateway page.",
  response: "The command_status returned in submit_sm_resp. Accept (ESME_ROK) gives the message an ID and a DLR. Any other code rejects it: no message ID and no DLR.",
  dlrStatus: "The stat: value in the delivery receipt, also sent as the message_state TLV. DELIVRD means delivered; UNDELIV, EXPIRED, REJECTD and others are failures.",
  dlrError: "The err: value in the delivery receipt (3 digits). Pick from the Error catalog; custom codes can also add a network_error_code TLV and replace the DLR text.",
  share: "Percentage of messages that get this result. All shares must add up to 100%.",

  // DLR settings
  dlrEnabled: "Send delivery receipts (deliver_sm with esm_class 0x04) to the client's RX or TRX sessions. A DLR is only sent if the client asked for one in registered_delivery.",
  dlrTiming: "When the DLR is sent after the submit_sm_resp: immediately, after a fixed delay, or after a random delay between a minimum and a maximum.",
  dlrIdFormat: "How the message ID is written in the DLR's id: field. Hex matches the ID in submit_sm_resp; some platforms expect decimal.",
  dlrDateSeconds: "Format of submit date: and done date: in the DLR text. Without seconds: YYMMDDhhmm. With seconds: YYMMDDhhmmss.",
  dlrIncludeText: "Add text: with the first 20 characters of the original message (or the DLR error's custom text) to the DLR.",
  dlrIntermediate: "An extra DLR with an in-progress status (ENROUTE or ACCEPTD) sent before the final one, to test how a platform handles intermediate receipts. When requested: only if the submit_sm sets registered_delivery bit 4 (0x10). Always: for every accepted message that asks for any DLR. The final DLR follows the normal timing and never arrives first.",
  dlrIntermediateStatus: "The stat: value of the intermediate DLR. ENROUTE (message_state 1): on its way. ACCEPTD (message_state 6): accepted by the network.",
  dlrIntermediateDelay: "Time after the submit_sm_resp before the intermediate DLR is sent. If it is longer than the final DLR's delay, the final DLR waits and follows right after it.",
  dlrIncludeTlvs: "Add the receipted_message_id, message_state and (if the error defines one) network_error_code TLVs to the DLR.",

  // limits
  tpsLimit: "Maximum submit_sm per second. Messages over the limit get ESME_RTHROTTLED (0x58). 0 means unlimited.",
  tpsScope: "Per bind: every session gets the full limit. Per gateway: all sessions of this gateway share one limit.",
  maxBinds: "Maximum number of sessions bound to this gateway at the same time. Further binds get ESME_RBINDFAIL. 0 means unlimited.",
  bindTypes: "Which bind types clients may use: TX (transmitter: sends messages), RX (receiver: gets DLRs and MO), TRX (transceiver: both).",
  responseLatency: "Artificial delay before each submit_sm_resp, to simulate a slow vendor. Saved but not applied yet: planned for a later phase.",

  // catalog
  commandStatus: "SMPP command_status codes. The SMPP 3.4 list is read-only; you can add vendor-specific codes in the range 0x400 to 0x4FF.",
  errCode: "The DLR err: code. Strictly numeric, stored as 3 digits (for example 901).",
  suggestedStatus: "The DLR status picked automatically when you choose this error in a DLR rule. You can still change it there.",
  networkError: "Optional network_error_code TLV sent with the DLR: a network type (3 = GSM) and an error code.",
  dlrText: "Optional text that replaces text: in the DLR when this error is used.",
  messageState: "The numeric message_state TLV value that matches each DLR status.",

  // live views
  accepted: "submit_sm answered with ESME_ROK per second, across all binds.",
  throttled: "submit_sm rejected with ESME_RTHROTTLED (0x58) per second because a TPS limit was exceeded.",
  rejected: "submit_sm rejected by the DLR rule or because the PDU was invalid, per second.",
  dlrsSent: "Delivery receipts sent to clients per second.",
  dlrScheduled: "DLRs waiting for their delay to pass before being sent.",
  dlrQueued: "DLRs waiting because no RX or TRX session is bound. They are kept for up to 72 hours and sent when a receiver binds.",
  sessions: "SMPP connections that are currently bound.",
  listening: "Gateways whose SMPP listener is up on their worker.",
  dlrsInFlight: "DLRs sent on this session that the client has not acknowledged with deliver_sm_resp yet.",
  workerDrain: "A draining worker keeps running its gateways but accepts no new ones. Move its gateways away before taking it down.",
  configVersion: "Increases on every change. Workers compare it every 60 seconds to make sure they run the latest settings.",

  // MO generator
  moGenerator: "Sends mobile-originated messages (deliver_sm with esm_class 0x00) to this gateway's RX and TRX sessions, as if subscribers were texting the platform. Each part of a long message counts as one message.",
  moMode: "Rate: a steady number of messages per second. Burst: a fixed number of messages, sent as fast as the receivers take them.",
  moStart: "Leave empty to start when you press Start. With a time, the generator waits until then.",
  moStop: "The run ends after this long or this many messages, whichever comes first. 0 means no limit; Stop ends it any time.",
  moSources: "The subscriber numbers the MOs come from: a list (picked at random), a numeric range, or a prefix followed by random digits.",
  moDestinations: "The short codes or long numbers the MOs are sent to, one per line, used in turn or at random.",
  moTemplates: "Message texts, picked by weight. Placeholders: {seq} message number in the run, {rand:N} N random digits, {keyword} a random keyword, {time} the time (UTC).",
  moLong: "Share of messages sent as long (concatenated) messages of 2 or 3 parts, each with a UDH.",
  moNoReceiver: "When no RX or TRX session is bound: Queue keeps the MOs (like DLRs, up to 72 hours) and sends them when a receiver binds; Drop discards them and counts them as dropped.",
  moTest: "Sends one MO right away to a bound RX or TRX session. It is never queued: with no receiver bound it fails.",

  // reports
  reportRetention: "Reports come from per-minute totals kept for 180 days. Bind history and message search use raw events, kept for 3 days.",
  ruleMix: "The share of messages each DLR rule entry actually got, against the share configured in the rule. Only messages that reached the rule count: throttled and invalid submit_sm never do.",
  dlrDelay: "Time from accepting a submit_sm to sending its final DLR (deliver_sm), for DLRs the client acknowledged. Intermediate DLRs are left out. Includes retry backoff if a first attempt failed.",
  dlrAcknowledged: "DLRs the client answered with deliver_sm_resp ESME_ROK.",
  dlrRetried: "DLRs sent again after an error deliver_sm_resp or no answer within 30 s (up to 5 times, with backoff).",
  dlrFailed: "DLRs given up after all retries.",
  licenceThrottled: "submit_sm rejected with ESME_RTHROTTLED because the licence's total TPS was exceeded. Counted from milestone M8 (licensing).",
  bindEvent: "Bound: a bind succeeded. Bind failed: wrong credentials, bind type or too many binds (status is the bind_resp error). Closed: the session ended. Killed: an admin ended it.",
  messageSearch: "Find a message by the ID the client received in submit_sm_resp (hex) or in the DLR's id: field (hex or decimal), or list recent traffic for a number: submit_sm and DLRs to it, and MOs from or to it. Covers the last 3 days.",
} as const;

export type GlossaryKey = keyof typeof GLOSSARY;
