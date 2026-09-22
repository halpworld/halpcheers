//! Secure account key storage for the Halp desktop app.
//!
//! SPECIFICATION (Decision 40 in docs/OPEN-QUESTIONS.md):
//! - Primary: Native OS keychain (macOS Keychain, Windows Credential Manager, Linux Secret Service).
//! - Fallback: Encrypted local configuration file using AES-256-GCM when keychain is unavailable.
//! - Plaintext storage of the account key on disk is strictly forbidden.

use std::fs;
use std::path::PathBuf;
use aes_gcm::{
    aead::{Aead, KeyInit},
    Aes256Gcm, Nonce,
};
use hkdf::Hkdf;
use sha2::Sha256;
use rand::{RngCore, thread_rng};

const SERVICE_NAME: &str = "to.halp.desktop";
const USER_KEY: &str = "account_key";
const HKDF_INFO: &[u8] = b"halp/desktop/fallback-v1";
const SALT_LEN: usize = 16;
const NONCE_LEN: usize = 12;

pub struct KeyStorage {
    config_dir: PathBuf,
}

impl KeyStorage {
    pub fn new(config_dir: PathBuf) -> Self {
        Self { config_dir }
    }

    /// Stores the 16-digit account key in OS keychain, falling back to encrypted file.
    pub fn store_key(&self, account_key: &str) -> Result<(), String> {
        let clean = account_key.replace(' ', "");
        if clean.len() != 16 || !clean.chars().all(|c| c.is_ascii_digit()) {
            return Err("Invalid 16-digit account key format".to_string());
        }

        // 1. Attempt native OS keychain via keyring crate
        if let Ok(entry) = keyring::Entry::new(SERVICE_NAME, USER_KEY) {
            if entry.set_password(&clean).is_ok() {
                return Ok(());
            }
        }

        // 2. Fallback: Encrypted local file storage via AES-256-GCM
        self.store_encrypted_fallback(&clean)
    }

    /// Retrieves the 16-digit account key from OS keychain or encrypted file.
    pub fn load_key(&self) -> Result<Option<String>, String> {
        // 1. Attempt native OS keychain
        if let Ok(entry) = keyring::Entry::new(SERVICE_NAME, USER_KEY) {
            if let Ok(pwd) = entry.get_password() {
                if pwd.len() == 16 && pwd.chars().all(|c| c.is_ascii_digit()) {
                    return Ok(Some(pwd));
                }
            }
        }

        // 2. Fallback: Encrypted local file
        self.load_encrypted_fallback()
    }

    /// Clears the account key on logout or account deletion.
    pub fn clear_key(&self) -> Result<(), String> {
        if let Ok(entry) = keyring::Entry::new(SERVICE_NAME, USER_KEY) {
            let _ = entry.delete_password();
        }

        let fallback_path = self.config_dir.join("account.enc");
        if fallback_path.exists() {
            let _ = fs::remove_file(fallback_path);
        }

        Ok(())
    }

    /// Derives a 32-byte encryption key for AES-256-GCM using HKDF-SHA256 from
    /// a machine-local seed and random salt.
    fn derive_cipher_key(&self, salt: &[u8]) -> [u8; 32] {
        let mut machine_seed = Vec::new();
        // Combine machine-specific environmental identifiers
        if let Ok(hostname) = std::env::var("HOSTNAME").or_else(|_| std::env::var("COMPUTERNAME")) {
            machine_seed.extend_from_slice(hostname.as_bytes());
        }
        if let Ok(user) = std::env::var("USER").or_else(|_| std::env::var("USERNAME")) {
            machine_seed.extend_from_slice(user.as_bytes());
        }
        machine_seed.extend_from_slice(self.config_dir.to_string_lossy().as_bytes());

        let hk = Hkdf::<Sha256>::new(Some(salt), &machine_seed);
        let mut okm = [0u8; 32];
        hk.expand(HKDF_INFO, &mut okm).expect("32 bytes is valid length for HKDF-SHA256");
        okm
    }

    fn store_encrypted_fallback(&self, key: &str) -> Result<(), String> {
        fs::create_dir_all(&self.config_dir).map_err(|e| e.to_string())?;
        let fallback_path = self.config_dir.join("account.enc");

        let mut rng = thread_rng();
        let mut salt = [0u8; SALT_LEN];
        let mut nonce_bytes = [0u8; NONCE_LEN];
        rng.fill_bytes(&mut salt);
        rng.fill_bytes(&mut nonce_bytes);

        let cipher_key = self.derive_cipher_key(&salt);
        let cipher = Aes256Gcm::new_from_slice(&cipher_key).map_err(|e| e.to_string())?;
        let nonce = Nonce::from_slice(&nonce_bytes);

        let ciphertext = cipher.encrypt(nonce, key.as_bytes())
            .map_err(|e| format!("Encryption error: {}", e))?;

        // Format: [16 bytes salt][12 bytes nonce][ciphertext + tag]
        let mut file_payload = Vec::with_capacity(SALT_LEN + NONCE_LEN + ciphertext.len());
        file_payload.extend_from_slice(&salt);
        file_payload.extend_from_slice(&nonce_bytes);
        file_payload.extend_from_slice(&ciphertext);

        fs::write(fallback_path, file_payload).map_err(|e| e.to_string())
    }

    fn load_encrypted_fallback(&self) -> Result<Option<String>, String> {
        let fallback_path = self.config_dir.join("account.enc");
        if !fallback_path.exists() {
            return Ok(None);
        }

        let payload = fs::read(fallback_path).map_err(|e| e.to_string())?;
        if payload.len() <= SALT_LEN + NONCE_LEN {
            return Ok(None);
        }

        let salt = &payload[0..SALT_LEN];
        let nonce_bytes = &payload[SALT_LEN..SALT_LEN + NONCE_LEN];
        let ciphertext = &payload[SALT_LEN + NONCE_LEN..];

        let cipher_key = self.derive_cipher_key(salt);
        let cipher = Aes256Gcm::new_from_slice(&cipher_key).map_err(|e| e.to_string())?;
        let nonce = Nonce::from_slice(nonce_bytes);

        let plaintext_bytes = cipher.decrypt(nonce, ciphertext)
            .map_err(|e| format!("Decryption error: {}", e))?;

        let key = String::from_utf8(plaintext_bytes).map_err(|e| e.to_string())?;
        if key.len() == 16 && key.chars().all(|c| c.is_ascii_digit()) {
            Ok(Some(key))
        } else {
            Ok(None)
        }
    }
}
