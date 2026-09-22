/**
 * Halp Web Application Controller.
 *
 * SPECIFICATION (docs/UI.md §2–§7, docs/SHARING.md, AGENTS.md):
 * - Key wall: 16 digits, "I have written it down" checkbox gating Continue.
 * - Login: 16 digits auto-grouping, uniform error message.
 * - Home: count scaling per docs/UI.md §8, no feed/timeline, escalation notice.
 * - Handles: create, share (QR SVG/PNG), pause toggle, burn dialog with verbatim copy.
 * - Settings: presets instead of numbers, quiet hours, upward-only escalation notice.
 * - Abuse reporting: terminal state verbatim copy.
 * - Send path: 300 ms floor, exactly two states ("sending", "Sent.").
 */

import {
  HalpClient,
  generateAccountKey,
  storeAccountKey,
  loadAccountKey,
  clearAccountKey,
  formatAccountKey,
  normalizeAccountKey,
  isAccountKeyShaped,
  isHandleShaped,
  HandleDTO,
  SettingsDTO,
  TransportManager,
} from '@halp/core';
import { QRCode } from './qr.js';

// Verbatim copy constants from docs/UI.md
export const COPY = {
  keyWall: {
    title: 'This is your account. There is no email, no password and no reset.',
    subtitle: 'If you lose it, the account is gone — handles, settings and all.',
    checkbox: 'I have written it down',
    copyBtn: 'Copy',
    downloadBtn: 'Download as text',
    continueBtn: 'Continue',
  },
  login: {
    failure: "That didn't work. Check the digits and try again.",
  },
  home: {
    escalationNotice:
      "This handle is unusually busy. We've widened its digest window so your notifications stay reasonable. You can change it below.",
    zeroCount: 'No appreciation pings yet today.',
    singleCount: 'Someone appreciates you.',
    pluralCount: (n: number) => `${n} people appreciate you.`,
  },
  burn: {
    prompt: (handle: string) =>
      `Burn ${handle}? Anyone with this link or QR code loses the ability to reach you, immediately and permanently. The handle is never reissued and this cannot be undone. Your other handles are unaffected.`,
    confirmBtn: 'Burn handle',
    cancelBtn: 'Cancel',
  },
  settings: {
    escalationDisclaimer:
      'If a handle gets flooded we may widen its window on its own. We will never make it narrower than you asked.',
    minCountLabel: 'Only tell me when at least N people have',
    digestPresets: [
      { label: 'Instant', seconds: 0 },
      { label: 'Every minute', seconds: 60 },
      { label: 'Every 15 min', seconds: 900 },
      { label: 'Hourly', seconds: 3600 },
      { label: 'Daily', seconds: 86400 },
    ],
    modePresets: [
      { label: 'Everything', value: 'all' as const },
      { label: 'Groups only', value: 'groups_only' as const },
      { label: 'Paused', value: 'paused' as const },
    ],
  },
  abuse: {
    terminalState: "Done. It should quieten down. If it doesn't, pause or burn this handle — that always works.",
  },
  send: {
    sending: 'Sending...',
    sent: 'Sent.',
    action: 'Appreciate them',
    error: 'Something went wrong. Try again.',
  },
};

export type AppView = 'key_wall' | 'login' | 'home' | 'settings' | 'share' | 'burn';

export class HalpApp {
  public client: HalpClient;
  public currentView: AppView = 'home';
  public currentKey: string | null = null;
  public handles: HandleDTO[] = [];
  public todayCount = 0;
  public settings: SettingsDTO | null = null;
  public activeModalHandle: HandleDTO | null = null;
  public transport: TransportManager | null = null;
  public container: HTMLElement;

  constructor(container: HTMLElement, client?: HalpClient) {
    this.container = container;
    this.client = client ?? new HalpClient();
  }

  async init(): Promise<void> {
    const savedKey = await loadAccountKey();
    if (savedKey && isAccountKeyShaped(savedKey)) {
      this.currentKey = savedKey;
      try {
        await this.client.loginWithAccountKey(savedKey);
        await this.loadData();
        this.currentView = 'home';
        this.initTransport();
      } catch {
        this.currentView = 'login';
      }
    } else {
      // First run: provision account key and show key wall
      const newKey = generateAccountKey();
      this.currentKey = newKey;
      this.currentView = 'key_wall';
    }
    this.render();
  }

  private initTransport(): void {
    if (this.transport) {
      this.transport.close();
    }
    this.transport = new TransportManager({
      client: this.client,
      events: {
        onPing: (count: number) => {
          this.todayCount += count;
          this.render();
        },
      },
    });
    this.transport.start();
  }

  async loadData(): Promise<void> {
    try {
      this.handles = await this.client.listHandles();
      this.settings = await this.client.getSettings();
      const pending = await this.client.getPending();
      if (pending) {
        this.todayCount = pending.n;
      }
    } catch {
      // Data load failures gracefully fall back
    }
  }

  // ---------------------------------------------------------------------------
  // View Renderers
  // ---------------------------------------------------------------------------

  render(): void {
    this.container.innerHTML = '';
    const main = document.createElement('main');
    main.className = 'container';

    // Live region for announcements (accessibility)
    const liveRegion = document.createElement('div');
    liveRegion.id = 'aria-live-status';
    liveRegion.className = 'sr-only';
    liveRegion.setAttribute('aria-live', 'polite');
    main.appendChild(liveRegion);

    if (this.currentView === 'key_wall') {
      main.appendChild(this.renderKeyWall());
    } else if (this.currentView === 'login') {
      main.appendChild(this.renderLogin());
    } else {
      main.appendChild(this.renderHeader());
      if (this.currentView === 'settings') {
        main.appendChild(this.renderSettings());
      } else {
        main.appendChild(this.renderHome());
      }
    }

    if (this.currentView === 'burn' && this.activeModalHandle) {
      main.appendChild(this.renderBurnModal(this.activeModalHandle));
    }
    if (this.currentView === 'share' && this.activeModalHandle) {
      main.appendChild(this.renderShareModal(this.activeModalHandle));
    }

    this.container.appendChild(main);
  }

  renderHeader(): HTMLElement {
    const header = document.createElement('header');
    header.className = 'app-header';

    const brand = document.createElement('div');
    brand.className = 'brand';
    brand.textContent = 'halp';

    const nav = document.createElement('nav');
    nav.className = 'nav-links';

    const homeLink = document.createElement('a');
    homeLink.href = '#';
    homeLink.textContent = 'Home';
    homeLink.onclick = (e) => { e.preventDefault(); this.currentView = 'home'; this.render(); };

    const settingsLink = document.createElement('a');
    settingsLink.href = '#';
    settingsLink.textContent = 'Settings';
    settingsLink.onclick = (e) => { e.preventDefault(); this.currentView = 'settings'; this.render(); };

    nav.appendChild(homeLink);
    nav.appendChild(settingsLink);
    header.appendChild(brand);
    header.appendChild(nav);
    return header;
  }

  // 1. Key Wall (§2)
  renderKeyWall(): HTMLElement {
    const card = document.createElement('div');
    card.className = 'card';

    const digitsDiv = document.createElement('div');
    digitsDiv.className = 'key-wall-digits';
    digitsDiv.textContent = this.currentKey ? formatAccountKey(this.currentKey) : '';

    const btnGroup = document.createElement('div');
    btnGroup.className = 'button-group';

    const copyBtn = document.createElement('button');
    copyBtn.type = 'button';
    copyBtn.className = 'btn btn-secondary';
    copyBtn.textContent = COPY.keyWall.copyBtn;
    copyBtn.onclick = () => {
      if (this.currentKey && navigator.clipboard) {
        navigator.clipboard.writeText(this.currentKey);
        copyBtn.textContent = 'Copied!';
        setTimeout(() => { copyBtn.textContent = COPY.keyWall.copyBtn; }, 2000);
      }
    };

    const dlBtn = document.createElement('button');
    dlBtn.type = 'button';
    dlBtn.className = 'btn btn-secondary';
    dlBtn.textContent = COPY.keyWall.downloadBtn;
    dlBtn.onclick = () => {
      if (this.currentKey) {
        const blob = new Blob([`Halp Account Key:\n${formatAccountKey(this.currentKey)}\n\nThere is no email, no password and no reset.\n`], { type: 'text/plain' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        a.href = url;
        a.download = 'halp-account-key.txt';
        a.click();
        URL.revokeObjectURL(url);
      }
    };

    btnGroup.appendChild(copyBtn);
    btnGroup.appendChild(dlBtn);

    const titleP = document.createElement('p');
    titleP.style.fontWeight = '600';
    titleP.textContent = COPY.keyWall.title;

    const subP = document.createElement('p');
    subP.className = 'caption';
    subP.textContent = COPY.keyWall.subtitle;

    const checkLabel = document.createElement('label');
    checkLabel.className = 'checkbox-group';

    const checkbox = document.createElement('input');
    checkbox.type = 'checkbox';
    checkbox.id = 'key-saved-checkbox';

    const checkText = document.createElement('span');
    checkText.textContent = COPY.keyWall.checkbox;
    checkLabel.appendChild(checkbox);
    checkLabel.appendChild(checkText);

    const continueBtn = document.createElement('button');
    continueBtn.type = 'button';
    continueBtn.className = 'btn';
    continueBtn.textContent = COPY.keyWall.continueBtn;
    continueBtn.disabled = true; // Disabled until checkbox is ticked

    checkbox.onchange = () => {
      continueBtn.disabled = !checkbox.checked;
    };

    continueBtn.onclick = async () => {
      if (this.currentKey) {
        await storeAccountKey(this.currentKey);
        try {
          await this.client.loginWithAccountKey(this.currentKey);
        } catch {
          // New account creation if not registered on server
          try {
            await this.client.createAccount();
            await this.client.loginWithAccountKey(this.currentKey);
          } catch {}
        }
        await this.loadData();
        this.currentView = 'home';
        this.initTransport();
        this.render();
      }
    };

    card.appendChild(digitsDiv);
    card.appendChild(btnGroup);
    card.appendChild(titleP);
    card.appendChild(subP);
    card.appendChild(checkLabel);
    card.appendChild(continueBtn);
    return card;
  }

  // 2. Login (§3)
  renderLogin(): HTMLElement {
    const card = document.createElement('div');
    card.className = 'card';

    const h1 = document.createElement('h1');
    h1.textContent = 'Log in';

    const form = document.createElement('form');
    form.className = 'form-group';

    const label = document.createElement('label');
    label.textContent = 'Account Key (16 digits)';
    label.htmlFor = 'login-key-input';

    const input = document.createElement('input');
    input.type = 'text';
    input.id = 'login-key-input';
    input.maxLength = 19; // 16 digits + 3 spaces
    input.placeholder = '6421 8830 5197 4462';

    input.oninput = () => {
      const clean = input.value.replace(/\D/g, '').slice(0, 16);
      input.value = formatAccountKey(clean);
    };

    const errorDiv = document.createElement('div');
    errorDiv.style.color = 'var(--danger)';
    errorDiv.style.fontSize = '0.9375rem';
    errorDiv.style.display = 'none';

    const submitBtn = document.createElement('button');
    submitBtn.type = 'submit';
    submitBtn.className = 'btn';
    submitBtn.textContent = 'Log in';

    form.onsubmit = async (e) => {
      e.preventDefault();
      errorDiv.style.display = 'none';
      const clean = input.value.replace(/\s+/g, '');
      if (!isAccountKeyShaped(clean)) {
        errorDiv.textContent = COPY.login.failure;
        errorDiv.style.display = 'block';
        return;
      }

      submitBtn.disabled = true;
      try {
        await this.client.loginWithAccountKey(clean);
        await storeAccountKey(clean);
        this.currentKey = clean;
        await this.loadData();
        this.currentView = 'home';
        this.initTransport();
        this.render();
      } catch {
        errorDiv.textContent = COPY.login.failure;
        errorDiv.style.display = 'block';
        submitBtn.disabled = false;
      }
    };

    form.appendChild(label);
    form.appendChild(input);
    form.appendChild(errorDiv);
    form.appendChild(submitBtn);

    card.appendChild(h1);
    card.appendChild(form);
    return card;
  }

  // 3. Home View (§4)
  renderHome(): HTMLElement {
    const section = document.createElement('section');
    section.style.display = 'flex';
    section.style.flexDirection = 'column';
    section.style.gap = '1.5rem';

    // Arrival Counter Card
    const countCard = document.createElement('div');
    countCard.className = 'card';

    const countTitle = document.createElement('h1');
    if (this.todayCount === 0) {
      countTitle.textContent = COPY.home.zeroCount;
    } else if (this.todayCount === 1) {
      countTitle.textContent = COPY.home.singleCount;
    } else {
      countTitle.textContent = COPY.home.pluralCount(this.todayCount);
    }

    countCard.appendChild(countTitle);
    section.appendChild(countCard);

    // Send Card (300 ms floor, two states: sending -> Sent.)
    const sendCard = document.createElement('div');
    sendCard.className = 'card';
    const sendTitle = document.createElement('h2');
    sendTitle.textContent = 'Send appreciation';

    const sendForm = document.createElement('form');
    sendForm.className = 'form-group';

    const targetInput = document.createElement('input');
    targetInput.type = 'text';
    targetInput.placeholder = 'Paste handle (e.g. e7k4p2m9qx3va) or alias';
    targetInput.required = true;

    const sendBtn = document.createElement('button');
    sendBtn.type = 'submit';
    sendBtn.className = 'btn';
    sendBtn.textContent = COPY.send.action;

    sendForm.onsubmit = async (e) => {
      e.preventDefault();
      const target = targetInput.value.trim();
      if (!target) return;

      sendBtn.disabled = true;
      sendBtn.textContent = COPY.send.sending;
      const start = performance.now();
      const minFloorMs = 300;

      try {
        await this.client.sendPing(target);
        const elapsed = performance.now() - start;
        const delay = Math.max(0, minFloorMs - elapsed);
        setTimeout(() => {
          sendBtn.textContent = COPY.send.sent;
          const live = document.getElementById('aria-live-status');
          if (live) live.textContent = COPY.send.sent;
          targetInput.value = '';
          setTimeout(() => {
            sendBtn.disabled = false;
            sendBtn.textContent = COPY.send.action;
          }, 2000);
        }, delay);
      } catch {
        const elapsed = performance.now() - start;
        const delay = Math.max(0, minFloorMs - elapsed);
        setTimeout(() => {
          sendBtn.disabled = false;
          sendBtn.textContent = COPY.send.action;
          const live = document.getElementById('aria-live-status');
          if (live) live.textContent = COPY.send.error;
        }, delay);
      }
    };

    sendForm.appendChild(targetInput);
    sendForm.appendChild(sendBtn);
    sendCard.appendChild(sendTitle);
    sendCard.appendChild(sendForm);
    section.appendChild(sendCard);

    // Handles Section
    const handlesCard = document.createElement('div');
    handlesCard.className = 'card';

    const handlesHeader = document.createElement('div');
    handlesHeader.style.display = 'flex';
    handlesHeader.style.justifyContent = 'space-between';
    handlesHeader.style.alignItems = 'center';

    const h2 = document.createElement('h2');
    h2.textContent = 'Your handles';

    const createBtn = document.createElement('button');
    createBtn.type = 'button';
    createBtn.className = 'btn btn-secondary';
    createBtn.textContent = '+ Create handle';
    createBtn.onclick = async () => {
      const label = prompt('Handle label:');
      if (label !== null) {
        const newH = await this.client.createHandle(label, 'personal');
        this.handles.unshift(newH);
        this.render();
      }
    };

    handlesHeader.appendChild(h2);
    handlesHeader.appendChild(createBtn);
    handlesCard.appendChild(handlesHeader);

    const listDiv = document.createElement('div');
    listDiv.className = 'handle-list';

    for (const h of this.handles) {
      listDiv.appendChild(this.renderHandleRow(h));
    }

    handlesCard.appendChild(listDiv);
    section.appendChild(handlesCard);

    return section;
  }

  renderHandleRow(h: HandleDTO): HTMLElement {
    const row = document.createElement('div');
    row.className = 'handle-item';

    const header = document.createElement('div');
    header.className = 'handle-item-header';

    const tag = document.createElement('span');
    tag.className = 'handle-tag';
    tag.textContent = h.handle;

    const kind = document.createElement('span');
    kind.className = 'handle-kind';
    kind.textContent = h.kind;

    header.appendChild(tag);
    header.appendChild(kind);

    if (h.label) {
      const labelSpan = document.createElement('div');
      labelSpan.style.fontSize = '0.9375rem';
      labelSpan.textContent = h.label;
      row.appendChild(labelSpan);
    }

    row.appendChild(header);

    const actions = document.createElement('div');
    actions.className = 'button-group';

    // Share action
    const shareBtn = document.createElement('button');
    shareBtn.type = 'button';
    shareBtn.className = 'btn btn-secondary';
    shareBtn.textContent = 'Share';
    shareBtn.onclick = () => {
      this.activeModalHandle = h;
      this.currentView = 'share';
      this.render();
    };

    // Pause toggle
    const pauseBtn = document.createElement('button');
    pauseBtn.type = 'button';
    pauseBtn.className = 'btn btn-secondary';
    pauseBtn.textContent = h.paused ? 'Resume' : 'Pause';
    pauseBtn.onclick = async () => {
      h.paused = !h.paused;
      await this.client.updateHandle(h.handle, { paused: h.paused });
      pauseBtn.textContent = h.paused ? 'Resume' : 'Pause';
    };

    // Report abuse action (§7)
    const abuseBtn = document.createElement('button');
    abuseBtn.type = 'button';
    abuseBtn.className = 'btn btn-secondary';
    abuseBtn.textContent = 'Report abuse';
    abuseBtn.onclick = async () => {
      if (confirm('Report abuse on this handle to mute top senders?')) {
        await this.client.reportAbuse(h.handle);
        alert(COPY.abuse.terminalState);
      }
    };

    // Burn action (§5)
    const burnBtn = document.createElement('button');
    burnBtn.type = 'button';
    burnBtn.className = 'btn btn-danger';
    burnBtn.textContent = 'Burn';
    burnBtn.onclick = () => {
      this.activeModalHandle = h;
      this.currentView = 'burn';
      this.render();
    };

    actions.appendChild(shareBtn);
    actions.appendChild(pauseBtn);
    actions.appendChild(abuseBtn);
    actions.appendChild(burnBtn);

    row.appendChild(actions);
    return row;
  }

  // 4. Burn Dialog (§5)
  renderBurnModal(h: HandleDTO): HTMLElement {
    const overlay = document.createElement('div');
    overlay.className = 'modal-overlay';

    const dialog = document.createElement('div');
    dialog.className = 'modal-dialog';
    dialog.setAttribute('role', 'alertdialog');

    const promptP = document.createElement('p');
    promptP.style.lineHeight = '1.5';
    promptP.textContent = COPY.burn.prompt(h.handle);

    const inputLabel = document.createElement('label');
    inputLabel.textContent = `Type "${h.handle}" to confirm:`;
    inputLabel.style.fontSize = '0.875rem';

    const input = document.createElement('input');
    input.type = 'text';

    const btnGroup = document.createElement('div');
    btnGroup.className = 'button-group';

    const cancelBtn = document.createElement('button');
    cancelBtn.type = 'button';
    cancelBtn.className = 'btn btn-secondary';
    cancelBtn.textContent = COPY.burn.cancelBtn;
    cancelBtn.onclick = () => {
      this.currentView = 'home';
      this.activeModalHandle = null;
      this.render();
    };

    const confirmBtn = document.createElement('button');
    confirmBtn.type = 'button';
    confirmBtn.className = 'btn btn-danger';
    confirmBtn.textContent = COPY.burn.confirmBtn;
    confirmBtn.disabled = true;

    input.oninput = () => {
      confirmBtn.disabled = input.value.trim() !== h.handle;
    };

    confirmBtn.onclick = async () => {
      await this.client.deleteHandle(h.handle);
      this.handles = this.handles.filter((item) => item.handle !== h.handle);
      this.currentView = 'home';
      this.activeModalHandle = null;
      this.render();
    };

    btnGroup.appendChild(cancelBtn);
    btnGroup.appendChild(confirmBtn);

    dialog.appendChild(promptP);
    dialog.appendChild(inputLabel);
    dialog.appendChild(input);
    dialog.appendChild(btnGroup);
    overlay.appendChild(dialog);
    return overlay;
  }

  // 5. Share Modal
  renderShareModal(h: HandleDTO): HTMLElement {
    const overlay = document.createElement('div');
    overlay.className = 'modal-overlay';

    const dialog = document.createElement('div');
    dialog.className = 'modal-dialog';

    const h2 = document.createElement('h2');
    h2.textContent = `Share ${h.handle}`;

    const link = `https://halp.to/h/${h.handle}`;
    const badgeMarkdown = `[![Halp](https://halp.to/badge/${h.handle}.svg)](${link})`;

    const qr = new QRCode(link);
    const qrDiv = document.createElement('div');
    qrDiv.style.display = 'flex';
    qrDiv.style.justifyContent = 'center';
    qrDiv.style.padding = '1rem';
    qrDiv.innerHTML = qr.toSVG(4);
    qrDiv.querySelector('svg')?.setAttribute('style', 'max-width: 180px; width: 100%; height: auto;');

    const copyLinkBtn = document.createElement('button');
    copyLinkBtn.type = 'button';
    copyLinkBtn.className = 'btn btn-secondary';
    copyLinkBtn.textContent = 'Copy Link';
    copyLinkBtn.onclick = () => {
      if (navigator.clipboard) navigator.clipboard.writeText(link);
      copyLinkBtn.textContent = 'Copied!';
      setTimeout(() => { copyLinkBtn.textContent = 'Copy Link'; }, 2000);
    };

    const copyBadgeBtn = document.createElement('button');
    copyBadgeBtn.type = 'button';
    copyBadgeBtn.className = 'btn btn-secondary';
    copyBadgeBtn.textContent = 'Copy Badge Markdown';
    copyBadgeBtn.onclick = () => {
      if (navigator.clipboard) navigator.clipboard.writeText(badgeMarkdown);
      copyBadgeBtn.textContent = 'Copied!';
      setTimeout(() => { copyBadgeBtn.textContent = 'Copy Badge Markdown'; }, 2000);
    };

    const closeBtn = document.createElement('button');
    closeBtn.type = 'button';
    closeBtn.className = 'btn';
    closeBtn.textContent = 'Done';
    closeBtn.onclick = () => {
      this.currentView = 'home';
      this.activeModalHandle = null;
      this.render();
    };

    dialog.appendChild(h2);
    dialog.appendChild(qrDiv);
    dialog.appendChild(copyLinkBtn);
    dialog.appendChild(copyBadgeBtn);
    dialog.appendChild(closeBtn);
    overlay.appendChild(dialog);
    return overlay;
  }

  // 6. Settings View (§6)
  renderSettings(): HTMLElement {
    const card = document.createElement('div');
    card.className = 'card';

    const h1 = document.createElement('h1');
    h1.textContent = 'Settings';

    const disclaimer = document.createElement('div');
    disclaimer.className = 'notice-box';
    disclaimer.textContent = COPY.settings.escalationDisclaimer;

    const form = document.createElement('form');
    form.className = 'form-group';

    // Digest window presets
    const digestLabel = document.createElement('label');
    digestLabel.textContent = 'Digest Window';

    const digestSelect = document.createElement('select');
    for (const preset of COPY.settings.digestPresets) {
      const opt = document.createElement('option');
      opt.value = preset.seconds.toString();
      opt.textContent = preset.label;
      if (this.settings && this.settings.digest_window_s === preset.seconds) {
        opt.selected = true;
      }
      digestSelect.appendChild(opt);
    }

    // Max per hour slider (1–60)
    const maxLabel = document.createElement('label');
    const currentMax = this.settings?.max_per_hour ?? 12;
    maxLabel.textContent = `Max notifications per hour: ${currentMax}`;

    const maxSlider = document.createElement('input');
    maxSlider.type = 'range';
    maxSlider.min = '1';
    maxSlider.max = '60';
    maxSlider.value = currentMax.toString();
    maxSlider.oninput = () => {
      maxLabel.textContent = `Max notifications per hour: ${maxSlider.value}`;
    };

    // Mode presets
    const modeLabel = document.createElement('label');
    modeLabel.textContent = 'Delivery Mode';

    const modeSelect = document.createElement('select');
    for (const m of COPY.settings.modePresets) {
      const opt = document.createElement('option');
      opt.value = m.value;
      opt.textContent = m.label;
      if (this.settings && this.settings.mode === m.value) {
        opt.selected = true;
      }
      modeSelect.appendChild(opt);
    }

    const saveBtn = document.createElement('button');
    saveBtn.type = 'submit';
    saveBtn.className = 'btn';
    saveBtn.textContent = 'Save Settings';

    form.onsubmit = async (e) => {
      e.preventDefault();
      saveBtn.disabled = true;
      await this.client.updateSettings({
        digest_window_s: parseInt(digestSelect.value, 10),
        max_per_hour: parseInt(maxSlider.value, 10),
        mode: modeSelect.value as 'all' | 'groups_only' | 'paused',
      });
      saveBtn.textContent = 'Saved!';
      setTimeout(() => {
        saveBtn.disabled = false;
        saveBtn.textContent = 'Save Settings';
      }, 1500);
    };

    form.appendChild(digestLabel);
    form.appendChild(digestSelect);
    form.appendChild(maxLabel);
    form.appendChild(maxSlider);
    form.appendChild(modeLabel);
    form.appendChild(modeSelect);
    form.appendChild(saveBtn);

    card.appendChild(h1);
    card.appendChild(disclaimer);
    card.appendChild(form);
    return card;
  }
}
