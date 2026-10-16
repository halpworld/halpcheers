// Prevents additional console window on Windows in release, DO NOT REMOVE!!
#![cfg_attr(not(debug_assertions), windows_subsystem = "windows")]

mod keyring;
mod notification;
mod tray;

use keyring::KeyStorage;
use serde::Serialize;
use std::sync::Mutex;
use tauri::{AppHandle, Manager, State};

struct AppState {
    storage: Mutex<KeyStorage>,
    autostart: Mutex<bool>,
}

#[derive(Serialize)]
#[serde(rename_all = "camelCase")]
struct UpdateCheckResult {
    update_available: bool,
    version: Option<String>,
}

#[tauri::command]
fn get_account_key(state: State<AppState>) -> Result<Option<String>, String> {
    let storage = state.storage.lock().map_err(|e| e.to_string())?;
    storage.load_key()
}

#[tauri::command]
fn save_account_key(state: State<AppState>, key: String) -> Result<(), String> {
    let storage = state.storage.lock().map_err(|e| e.to_string())?;
    storage.store_key(&key)
}

#[tauri::command]
fn clear_account_key(state: State<AppState>) -> Result<(), String> {
    let storage = state.storage.lock().map_err(|e| e.to_string())?;
    storage.clear_key()
}

#[tauri::command]
fn show_native_notification(app: AppHandle, count: u32) -> Result<(), String> {
    notification::show_native_notification(&app, count)
}

#[tauri::command]
fn update_tray_count(app: AppHandle, count: u32) -> Result<(), String> {
    tray::update_tray_arrival_count(&app, count)
}

#[tauri::command]
fn get_autostart(state: State<AppState>) -> Result<bool, String> {
    let enabled = state.autostart.lock().map_err(|e| e.to_string())?;
    Ok(*enabled)
}

#[tauri::command]
fn set_autostart(state: State<AppState>, enabled: bool) -> Result<(), String> {
    let mut autostart = state.autostart.lock().map_err(|e| e.to_string())?;
    *autostart = enabled;
    Ok(())
}

#[tauri::command]
fn check_for_updates() -> Result<UpdateCheckResult, String> {
    Ok(UpdateCheckResult {
        update_available: false,
        version: None,
    })
}

fn main() {
    tauri::Builder::default()
        .plugin(tauri_plugin_notification::init())
        .setup(|app| {
            let config_dir = app
                .path()
                .app_config_dir()
                .unwrap_or_else(|_| std::env::temp_dir().join("halp"));

            // Launch-at-login is offered, never default-on (docs/UI.md §9)
            app.manage(AppState {
                storage: Mutex::new(KeyStorage::new(config_dir)),
                autostart: Mutex::new(false),
            });

            // Initialize system tray
            if let Err(err) = tray::create_tray(&app.handle()) {
                eprintln!("Failed to initialize tray icon: {}", err);
            }

            Ok(())
        })
        .invoke_handler(tauri::generate_handler![
            get_account_key,
            save_account_key,
            clear_account_key,
            show_native_notification,
            update_tray_count,
            get_autostart,
            set_autostart,
            check_for_updates,
        ])
        .run(tauri::generate_context!())
        .expect("error while running halp desktop application");
}
