//! Native OS notification dispatch for the Halp desktop app.
//!
//! SPECIFICATION (docs/UI.md §8):
//! - Same copy bands across Service Worker, Desktop app, and TUI:
//!   - n = 1: "Someone appreciates you."
//!   - n = 2..20: "{n} people appreciate you."
//!   - n >= 21: "{n} people appreciate you."
//! - No body text, no action buttons, no reply affordance, no sender line.
//! - Native OS notification on macOS, Windows, Linux via tauri-plugin-notification.

use tauri::{AppHandle, Runtime};
use tauri_plugin_notification::NotificationExt;

/// Formats the notification title strictly matching docs/UI.md §8.
pub fn format_notification_title(n: u32) -> String {
    match n {
        0 | 1 => "Someone appreciates you.".to_string(),
        count => format!("{} people appreciate you.", count),
    }
}

/// Dispatches a native OS notification with the exact copy band, with zero body and zero sender info.
pub fn show_native_notification<R: Runtime>(app: &AppHandle<R>, count: u32) -> Result<(), String> {
    let title = format_notification_title(count);

    app.notification()
        .builder()
        .title(title)
        .show()
        .map_err(|e| format!("Failed to show native notification: {}", e))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_notification_copy_bands() {
        assert_eq!(format_notification_title(1), "Someone appreciates you.");
        assert_eq!(format_notification_title(2), "2 people appreciate you.");
        assert_eq!(format_notification_title(7), "7 people appreciate you.");
        assert_eq!(format_notification_title(20), "20 people appreciate you.");
        assert_eq!(format_notification_title(21), "21 people appreciate you.");
        assert_eq!(format_notification_title(412), "412 people appreciate you.");
    }
}
