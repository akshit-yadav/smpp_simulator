"""Gateway password encryption, admin password hashing and session tokens."""

import base64
import binascii
import hashlib
import os
import secrets

from argon2 import PasswordHasher
from argon2.exceptions import InvalidHashError, VerificationError
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

_NONCE = 12


class Cipher:
    """AES-256-GCM. Gateway passwords are encrypted, not hashed: the plain value
    is needed by workers to check binds. Blob layout: nonce (12) || ciphertext+tag."""

    def __init__(self, key_b64: str) -> None:
        try:
            key = base64.b64decode(key_b64, validate=True)
        except (binascii.Error, ValueError) as exc:
            raise ValueError("FSG_SECRET_KEY must be base64") from exc
        if len(key) != 32:
            raise ValueError("FSG_SECRET_KEY must decode to 32 bytes (openssl rand -base64 32)")
        self._aead = AESGCM(key)

    def encrypt(self, plaintext: str) -> bytes:
        nonce = os.urandom(_NONCE)
        return nonce + self._aead.encrypt(nonce, plaintext.encode(), None)

    def decrypt(self, blob: bytes) -> str:
        return self._aead.decrypt(blob[:_NONCE], blob[_NONCE:], None).decode()


_hasher = PasswordHasher()
# Verified when the username is unknown, so both paths take the same time.
_DUMMY_HASH = _hasher.hash(secrets.token_urlsafe(16))


def hash_password(password: str) -> str:
    return _hasher.hash(password)


def verify_password(stored_hash: str | None, password: str) -> bool:
    try:
        return _hasher.verify(stored_hash or _DUMMY_HASH, password) and stored_hash is not None
    except (VerificationError, InvalidHashError):
        return False


def new_token() -> str:
    return secrets.token_urlsafe(32)


def token_hash(token: str) -> str:
    return hashlib.sha256(token.encode()).hexdigest()
