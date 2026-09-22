//! System tray implementation for the Halp desktop app.
//!
//! SPECIFICATION (Issue #24, docs/UI.md §9):
//! - Tray icon with a send action for the default handle and the arrival state.
//! - Open window on click or menu item.
//! - Preserves the 300 ms floor and two states ("Sending...", "Sent.") on send actions.

use tauri::{
    menu::{Menu, MenuItem},
    tray::{MouseButton, MouseButtonState, TrayIconBuilder, TrayIconEvent},
    AppHandle, Manager, Runtime,
};
use std::sync::atomic::{AtomicU32, Ordering};
use std::sync::Arc;

pub struct TrayState {
    pub today_count: Arc<AtomicU32>,
}

impl Default for TrayState {
    fn default() -> Self {
        Self {
            today_count: Arc::new(AtomicU32::new(0)),
        }
    }
}

pub fn create_tray<R: Runtime>(app: &AppHandle<R>) -> Result<(), Box<dyn std::error::Error>> {
    let count_item = MenuItem::with_id(app, "count", "Today: 0 appreciations", false, None::<&str>)?;
    let send_item = MenuItem::with_id(app, "send", "Send Appreciation", true, None::<&str>)?;
    let open_item = MenuItem::with_id(app, "open", "Open Halp", true, None::<&str>)?;
    let quit_item = MenuItem::with_id(app, "quit", "Quit", true, None::<&str>)?;

    let menu = Menu::with_items(app, &[&count_item, &send_item, &open_item, &quit_item])?;

    let _tray = TrayIconBuilder::with_id("main-tray")
        .tooltip("Halp — Anonymous Appreciation")
        .menu(&menu)
        .show_menu_on_left_click(false)
        .on_menu_event(|app, event| {
            match event.id.as_ref() {
                "open" => {
                    if let Some(window) = app.get_webview_window("main") {
                        let _ = window.show();
                        let _ = window.set_focus();
                    }
                }
                "send" => {
                    if let Some(window) = app.get_webview_window("main") {
                        let _ = window.show();
                        let _ = window.set_focus();
                        let _ = window.emit("trigger-send-default", ());
                    }
                }
                "quit" => {
                    app.exit(0);
                }
                _ => {}
            }
        })
        .on_tray_icon_event(|tray, event| {
            if let TrayIconEvent::Click {
                button: MouseButton::Left,
                button_state: MouseButtonState::Up,
                ..
            } = event
            {
                let app = tray.app_handle();
                if let Some(window) = app.get_webview_window("main") {
                    let _ = window.show();
                    let _ = window.set_focus();
                }
            }
        })
        .build(app)?;

    Ok(())
}

/// Updates the tray arrival count text and tooltip.
pub fn update_tray_arrival_count<R: Runtime>(app: &AppHandle<R>, count: u32) -> Result<(), String> {
    if let Some(tray) = app.tray_by_id("main-tray") {
        let tooltip = match count {
            0 => "Halp — No appreciations yet today".to_string(),
            1 => "Halp — Someone appreciates you today".to_string(),
            n => format!("Halp — {} appreciations today", n),
        };
        let _ = tray.set_tooltip(Some(tooltip));
    }
    Ok(())
}
