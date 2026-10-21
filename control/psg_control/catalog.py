"""Built-in catalogs: SMPP 3.4 command_status, DLR statuses and system DLR errors."""

# SMPP 3.4 section 5.1.3. Read-only; admins may add vendor codes 0x400-0x4FF only.
COMMAND_STATUSES: list[tuple[int, str, str]] = [
    (0x00, "ESME_ROK", "No Error"),
    (0x01, "ESME_RINVMSGLEN", "Message Length is invalid"),
    (0x02, "ESME_RINVCMDLEN", "Command Length is invalid"),
    (0x03, "ESME_RINVCMDID", "Invalid Command ID"),
    (0x04, "ESME_RINVBNDSTS", "Incorrect BIND Status for given command"),
    (0x05, "ESME_RALYBND", "ESME Already in Bound State"),
    (0x06, "ESME_RINVPRTFLG", "Invalid Priority Flag"),
    (0x07, "ESME_RINVREGDLVFLG", "Invalid Registered Delivery Flag"),
    (0x08, "ESME_RSYSERR", "System Error"),
    (0x0A, "ESME_RINVSRCADR", "Invalid Source Address"),
    (0x0B, "ESME_RINVDSTADR", "Invalid Dest Addr"),
    (0x0C, "ESME_RINVMSGID", "Message ID is invalid"),
    (0x0D, "ESME_RBINDFAIL", "Bind Failed"),
    (0x0E, "ESME_RINVPASWD", "Invalid Password"),
    (0x0F, "ESME_RINVSYSID", "Invalid System ID"),
    (0x11, "ESME_RCANCELFAIL", "Cancel SM Failed"),
    (0x13, "ESME_RREPLACEFAIL", "Replace SM Failed"),
    (0x14, "ESME_RMSGQFUL", "Message Queue Full"),
    (0x15, "ESME_RINVSERTYP", "Invalid Service Type"),
    (0x33, "ESME_RINVNUMDESTS", "Invalid number of destinations"),
    (0x34, "ESME_RINVDLNAME", "Invalid Distribution List name"),
    (0x40, "ESME_RINVDESTFLAG", "Destination flag is invalid (submit_multi)"),
    (0x42, "ESME_RINVSUBREP", "Invalid 'submit with replace' request"),
    (0x43, "ESME_RINVESMCLASS", "Invalid esm_class field data"),
    (0x44, "ESME_RCNTSUBDL", "Cannot Submit to Distribution List"),
    (0x45, "ESME_RSUBMITFAIL", "submit_sm or submit_multi failed"),
    (0x48, "ESME_RINVSRCTON", "Invalid Source address TON"),
    (0x49, "ESME_RINVSRCNPI", "Invalid Source address NPI"),
    (0x50, "ESME_RINVDSTTON", "Invalid Destination address TON"),
    (0x51, "ESME_RINVDSTNPI", "Invalid Destination address NPI"),
    (0x53, "ESME_RINVSYSTYP", "Invalid system_type field"),
    (0x54, "ESME_RINVREPFLAG", "Invalid replace_if_present flag"),
    (0x55, "ESME_RINVNUMMSGS", "Invalid number of messages"),
    (0x58, "ESME_RTHROTTLED", "Throttling error (ESME has exceeded allowed message limits)"),
    (0x61, "ESME_RINVSCHED", "Invalid Scheduled Delivery Time"),
    (0x62, "ESME_RINVEXPIRY", "Invalid message validity period (Expiry time)"),
    (0x63, "ESME_RINVDFTMSGID", "Predefined Message Invalid or Not Found"),
    (0x64, "ESME_RX_T_APPN", "ESME Receiver Temporary App Error Code"),
    (0x65, "ESME_RX_P_APPN", "ESME Receiver Permanent App Error Code"),
    (0x66, "ESME_RX_R_APPN", "ESME Receiver Reject Message Error Code"),
    (0x67, "ESME_RQUERYFAIL", "query_sm request failed"),
    (0xC0, "ESME_RINVOPTPARSTREAM", "Error in the optional part of the PDU Body"),
    (0xC1, "ESME_ROPTPARNOTALLWD", "Optional Parameter not allowed"),
    (0xC2, "ESME_RINVPARLEN", "Invalid Parameter Length"),
    (0xC3, "ESME_RMISSINGOPTPARAM", "Expected Optional Parameter missing"),
    (0xC4, "ESME_RINVOPTPARAMVAL", "Invalid Optional Parameter Value"),
    (0xFE, "ESME_RDELIVERYFAILURE", "Delivery Failure (used for data_sm_resp)"),
    (0xFF, "ESME_RUNKNOWNERR", "Unknown Error"),
]

VENDOR_MIN, VENDOR_MAX = 0x400, 0x4FF

# DLR stat: values and their message_state TLV values.
DLR_STATUSES: dict[str, int] = {
    "ENROUTE": 1,
    "DELIVRD": 2,
    "EXPIRED": 3,
    "DELETED": 4,
    "UNDELIV": 5,
    "ACCEPTD": 6,
    "UNKNOWN": 7,
    "REJECTD": 8,
}

# err: codes. (code, label, suggested status, GSM MAP error code or None).
SYSTEM_DLR_ERRORS: list[tuple[str, str, str, int | None]] = [
    ("000", "No error", "DELIVRD", None),
    ("001", "Unknown subscriber", "UNDELIV", 1),
    ("005", "Unidentified subscriber", "UNDELIV", 5),
    ("006", "Absent subscriber (SM)", "EXPIRED", 6),
    ("009", "Illegal subscriber", "UNDELIV", 9),
    ("011", "Teleservice not provisioned", "UNDELIV", 11),
    ("012", "Illegal equipment", "UNDELIV", 12),
    ("013", "Call barred", "UNDELIV", 13),
    ("021", "Facility not supported", "UNDELIV", 21),
    ("027", "Absent subscriber", "EXPIRED", 27),
    ("031", "Subscriber busy for MT-SMS", "EXPIRED", 31),
    ("032", "SM delivery failure", "UNDELIV", 32),
    ("034", "System failure", "UNDELIV", 34),
    ("035", "Data missing", "UNDELIV", 35),
    ("036", "Unexpected data value", "UNDELIV", 36),
]

# Example custom codes from the design, seeded as deletable custom entries.
SAMPLE_CUSTOM_DLR_ERRORS: list[tuple[str, str, str, str]] = [
    ("T01", "Template Mismatch", "REJECTD", "Template Mismatch"),
    ("G01", "Gateway Closed", "UNDELIV", "Gateway Closed"),
]

GSM_NETWORK_TYPE = 3
