/**
 * Desktop Tauri IPC Bridge.
 *
 * Exposes account keychain storage, native OS notifications, and tray synchronization
 * to the desktop webview, with graceful memory-backed fallback for tests and headless environments.
 */

export interface DesktopBridge {
  getAccountKey(): Promise<string | null>;
  saveAccountKey(key: string): Promise<void>;
  clearAccountKey(): Promise<void>;
  showNativeNotification(count: number): Promise<void>;
  updateTrayCount(count: number): Promise<void>;
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
}

/**
 * Mock bridge for headless unit testing and environments without Tauri.
 */
export class MockDesktopBridge implements DesktopBridge {
  private key: string | null = null;
  public notificationHistory: number[] = [];
  public trayCountHistory: number[] = [];

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
