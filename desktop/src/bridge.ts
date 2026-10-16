/**
 * Desktop Tauri IPC Bridge.
 *
 * Exposes account keychain storage, native OS notifications, tray synchronization,
 * launch-at-login autostart (never default-on per docs/UI.md §9), and auto-update
 * checks to the desktop webview, with graceful memory-backed fallback for tests.
 */

export interface UpdateCheckResult {
  updateAvailable: boolean;
  version?: string;
}

export interface DesktopBridge {
  getAccountKey(): Promise<string | null>;
  saveAccountKey(key: string): Promise<void>;
  clearAccountKey(): Promise<void>;
  showNativeNotification(count: number): Promise<void>;
  updateTrayCount(count: number): Promise<void>;
  getAutostart(): Promise<boolean>;
  setAutostart(enabled: boolean): Promise<void>;
  checkForUpdates(): Promise<UpdateCheckResult>;
}

declare global {
  interface Window {
    __TAURI_INTERNALS__?: {
      invoke<T = unknown>(cmd: string, args?: Record<string, unknown>): Promise<T>;
    };
    __TAURI__?: {
      core?: {
        invoke<T = unknown>(cmd: string, args?: Record<string, unknown>): Promise<T>;
      };
    };
  }
}

class TauriBridge implements DesktopBridge {
  private async invoke<T>(cmd: string, args?: Record<string, unknown>): Promise<T> {
    if (typeof window !== 'undefined') {
      if (window.__TAURI_INTERNALS__?.invoke) {
        return window.__TAURI_INTERNALS__.invoke<T>(cmd, args);
      }
      if (window.__TAURI__?.core?.invoke) {
        return window.__TAURI__.core.invoke<T>(cmd, args);
      }
    }
    throw new Error('Tauri IPC runtime not available');
  }

  async getAccountKey(): Promise<string | null> {
    return this.invoke<string | null>('get_account_key');
  }

  async saveAccountKey(key: string): Promise<void> {
    return this.invoke<void>('save_account_key', { key });
  }

  async clearAccountKey(): Promise<void> {
    return this.invoke<void>('clear_account_key');
  }

  async showNativeNotification(count: number): Promise<void> {
    return this.invoke<void>('show_native_notification', { count });
  }

  async updateTrayCount(count: number): Promise<void> {
    return this.invoke<void>('update_tray_count', { count });
  }

  async getAutostart(): Promise<boolean> {
    return this.invoke<boolean>('get_autostart');
  }

  async setAutostart(enabled: boolean): Promise<void> {
    return this.invoke<void>('set_autostart', { enabled });
  }

  async checkForUpdates(): Promise<UpdateCheckResult> {
    return this.invoke<UpdateCheckResult>('check_for_updates');
  }
}

/**
 * Mock bridge for headless unit testing and environments without Tauri.
 */
export class MockDesktopBridge implements DesktopBridge {
  private key: string | null = null;
  private autostart = false; // Launch-at-login is offered, never default-on (docs/UI.md §9)
  public notificationHistory: number[] = [];
  public trayCountHistory: number[] = [];
  public updateResult: UpdateCheckResult = { updateAvailable: false };

  async getAccountKey(): Promise<string | null> {
    return this.key;
  }

  async saveAccountKey(key: string): Promise<void> {
    this.key = key;
  }

  async clearAccountKey(): Promise<void> {
    this.key = null;
  }

  async showNativeNotification(count: number): Promise<void> {
    this.notificationHistory.push(count);
  }

  async updateTrayCount(count: number): Promise<void> {
    this.trayCountHistory.push(count);
  }

  async getAutostart(): Promise<boolean> {
    return this.autostart;
  }

  async setAutostart(enabled: boolean): Promise<void> {
    this.autostart = enabled;
  }

  async checkForUpdates(): Promise<UpdateCheckResult> {
    return this.updateResult;
  }
}

export function createDesktopBridge(): DesktopBridge {
  if (
    typeof window !== 'undefined' &&
    (window.__TAURI_INTERNALS__?.invoke || window.__TAURI__?.core?.invoke)
  ) {
    return new TauriBridge();
  }
  return new MockDesktopBridge();
}
