/**
 * Cross-browser extension API shim for Chrome and Firefox MV3.
 */

declare const chrome: any;
declare const browser: any;

export const extensionApi = {
  storage: {
    async get(key: string): Promise<string | null> {
      if (typeof browser !== 'undefined' && browser.storage?.local) {
        const res = await browser.storage.local.get(key);
        return res[key] ?? null;
      }
      if (typeof chrome !== 'undefined' && chrome.storage?.local) {
        return new Promise((resolve) => {
          chrome.storage.local.get([key], (res: any) => {
            resolve(res ? res[key] ?? null : null);
          });
        });
      }
      return null;
    },

    async set(key: string, value: string): Promise<void> {
      if (typeof browser !== 'undefined' && browser.storage?.local) {
        await browser.storage.local.set({ [key]: value });
        return;
      }
      if (typeof chrome !== 'undefined' && chrome.storage?.local) {
        return new Promise((resolve) => {
          chrome.storage.local.set({ [key]: value }, () => resolve());
        });
      }
    },

    async remove(key: string): Promise<void> {
      if (typeof browser !== 'undefined' && browser.storage?.local) {
        await browser.storage.local.remove(key);
        return;
      }
      if (typeof chrome !== 'undefined' && chrome.storage?.local) {
        return new Promise((resolve) => {
          chrome.storage.local.remove([key], () => resolve());
        });
      }
    },
  },

  setBadge(text: string, color: string = '#0070f3'): void {
    const api = typeof browser !== 'undefined' ? browser.action : typeof chrome !== 'undefined' ? chrome.action : null;
    if (api) {
      api.setBadgeText({ text });
      api.setBadgeBackgroundColor({ color });
    }
  },

  showNotification(title: string, message: string = ''): void {
    const api = typeof browser !== 'undefined' ? browser.notifications : typeof chrome !== 'undefined' ? chrome.notifications : null;
    if (api) {
      api.create({
        type: 'basic',
        iconUrl: 'icon.png',
        title,
        message,
        priority: 1,
      });
    }
  },
};
