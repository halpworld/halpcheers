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
  landing: {
    heroTitle: 'Someone appreciates you.',
    heroSubtitle:
      'A quiet, heartfelt way to share positive energy with workmates, friends, and creators around the world. No reply pressure, no unsolicited noise, no awkwardness — just the honest, uplifting feeling of knowing your work and presence are appreciated.',
    getStartedBtn: 'Start spreading appreciation',
    loginBtn: 'Log in with key',
    pills: [
      '☀️ Spread happiness',
      '💛 Lift teammates up',
      '🌱 Zero reply pressure',
      '✨ Pure positive appreciation',
    ],
  },
};

export type AppView = 'landing' | 'key_wall' | 'login' | 'home' | 'settings' | 'share' | 'burn';

export class HalpApp {
  public client: HalpClient;
  public currentView: AppView = 'landing';
  public currentKey: string | null = null;
  public handles: HandleDTO[] = [];
  public todayCount = 0;
  public settings: SettingsDTO | null = null;
  public activeModalHandle: HandleDTO | null = null;
  public transport: TransportManager | null = null;
  public container: HTMLElement;
  public activePreviewTab = 0;
  public isMobileMenuOpen = false;

  constructor(container: HTMLElement, client?: HalpClient) {
    this.container = container;
    this.client = client ?? new HalpClient();
  }

  async init(): Promise<void> {
    const savedKey = await loadAccountKey();
    if (savedKey && isAccountKeyShaped(savedKey)) {
      this.currentKey = savedKey;
    }

    if (typeof window !== 'undefined') {
      window.addEventListener('hashchange', () => this.handleHashChange());
      window.addEventListener('resize', () => {
        if (window.innerWidth > 1040 && this.isMobileMenuOpen) {
          this.toggleMobileMenu(false);
        }
      });
      window.addEventListener('keydown', (e: KeyboardEvent) => {
        if (e.key === 'Escape' && this.isMobileMenuOpen) {
          this.toggleMobileMenu(false);
          const toggleBtn = this.container.querySelector('#mobile-menu-toggle') as HTMLElement | null;
          if (toggleBtn && typeof toggleBtn.focus === 'function') {
            toggleBtn.focus();
          }
        }
      });
      window.addEventListener('click', (e: MouseEvent) => {
        if (this.isMobileMenuOpen) {
          const header = this.container.querySelector('.landing-header');
          if (header && typeof header.contains === 'function' && !header.contains(e.target as Node)) {
            this.toggleMobileMenu(false);
          }
        }
      });
    }

    const hash = typeof window !== 'undefined' ? window.location.hash : '';
    if (hash === '#app' || hash === '#home') {
      if (this.currentKey) {
        try {
          await this.client.loginWithAccountKey(this.currentKey);
          await this.loadData();
          this.currentView = 'home';
          this.initTransport();
        } catch {
          this.currentView = 'home';
        }
      } else {
        this.currentView = 'login';
      }
    } else if (hash === '#login') {
      this.currentView = 'login';
    } else if (hash === '#register' || hash === '#key_wall') {
      if (!this.currentKey) this.currentKey = generateAccountKey();
      this.currentView = 'key_wall';
    } else {
      // Default for root / is always the landing page
      this.currentView = 'landing';
    }

    this.render();

    const landingSections = ['#why-it-matters', '#showcase', '#how-it-works', '#security'];
    if (landingSections.includes(hash)) {
      this.navigateToSection(hash);
    }
  }

  handleHashChange(): void {
    const hash = typeof window !== 'undefined' ? window.location.hash : '';
    const landingSections = ['#why-it-matters', '#showcase', '#how-it-works', '#security'];
    if (this.currentView === 'landing' && landingSections.includes(hash)) {
      this.navigateToSection(hash);
      return;
    }

    if (hash === '#app' || hash === '#home') {
      if (this.currentKey) {
        this.currentView = 'home';
      } else {
        this.currentView = 'login';
      }
    } else if (hash === '#login') {
      this.currentView = 'login';
    } else if (hash === '#register' || hash === '#key_wall') {
      if (!this.currentKey) this.currentKey = generateAccountKey();
      this.currentView = 'key_wall';
    } else {
      this.currentView = 'landing';
    }
    this.render();
  }

  toggleMobileMenu(open?: boolean): void {
    this.isMobileMenuOpen = open !== undefined ? open : !this.isMobileMenuOpen;
    const menu = this.container.querySelector('#landing-mobile-menu') as HTMLElement | null;
    const toggleBtn = this.container.querySelector('#mobile-menu-toggle') as HTMLElement | null;
    if (menu && toggleBtn) {
      if (this.isMobileMenuOpen) {
        menu.className = 'landing-mobile-menu open';
        menu.setAttribute('aria-hidden', 'false');
        toggleBtn.setAttribute('aria-expanded', 'true');
        toggleBtn.innerHTML = `
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <line x1="18" y1="6" x2="6" y2="18"></line>
            <line x1="6" y1="6" x2="18" y2="18"></line>
          </svg>
        `;
      } else {
        menu.className = 'landing-mobile-menu';
        menu.setAttribute('aria-hidden', 'true');
        toggleBtn.setAttribute('aria-expanded', 'false');
        toggleBtn.innerHTML = `
          <svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
            <line x1="3" y1="6" x2="21" y2="6"></line>
            <line x1="3" y1="12" x2="21" y2="12"></line>
            <line x1="3" y1="18" x2="21" y2="18"></line>
          </svg>
        `;
      }
    }
  }

  navigateToSection(sectionId: string): void {
    if (this.isMobileMenuOpen) {
      this.toggleMobileMenu(false);
    }
    if (this.currentView !== 'landing') {
      this.currentView = 'landing';
      this.render();
    }
    const cleanId = sectionId.replace(/^#/, '');
    if (typeof document !== 'undefined') {
      const target = document.getElementById ? document.getElementById(cleanId) : null;
      if (target && 'scrollIntoView' in target) {
        target.scrollIntoView({ behavior: 'smooth' });
        if (typeof window !== 'undefined' && window.history?.pushState) {
          window.history.pushState(null, '', `#${cleanId}`);
        }
      }
    }
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
    main.className = this.currentView === 'landing' ? 'container landing-container' : 'container';

    // Live region for announcements (accessibility)
    const liveRegion = document.createElement('div');
    liveRegion.id = 'aria-live-status';
    liveRegion.className = 'sr-only';
    liveRegion.setAttribute('aria-live', 'polite');
    main.appendChild(liveRegion);

    if (this.currentView === 'landing') {
      main.appendChild(this.renderLanding());
    } else if (this.currentView === 'key_wall') {
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

  // ---------------------------------------------------------------------------
  // Landing Page Renderer
  // ---------------------------------------------------------------------------

  // ---------------------------------------------------------------------------
  // Landing Page Renderer
  // ---------------------------------------------------------------------------

  renderLanding(): HTMLElement {
    const landing = document.createElement('div');
    landing.className = 'landing-page';

    landing.appendChild(this.renderLandingHeader());
    landing.appendChild(this.renderHero());
    landing.appendChild(this.renderWhyAppreciationMatters());
    landing.appendChild(this.renderAppShowcase());
    landing.appendChild(this.renderHowItWorks());
    landing.appendChild(this.renderSafeAndRespectful());
    landing.appendChild(this.renderQuickAccess());
    landing.appendChild(this.renderLandingFooter());

    return landing;
  }

  renderLandingHeader(): HTMLElement {
    const header = document.createElement('header');
    header.className = 'landing-header';

    const headerBar = document.createElement('div');
    headerBar.className = 'landing-header-bar';

    // 1. Brand Group
    const brandGroup = document.createElement('div');
    brandGroup.className = 'brand-group';

    const brand = document.createElement('a');
    brand.href = '#';
    brand.className = 'brand brand-link';
    brand.innerHTML = '<span class="brand-spark">💛</span> <span class="brand-text">halp</span>';
    brand.onclick = (e) => {
      e.preventDefault();
      if (this.currentView === 'landing') {
        if (typeof window !== 'undefined') {
          window.scrollTo({ top: 0, behavior: 'smooth' });
          if (window.history?.pushState) window.history.pushState(null, '', ' ');
        }
        if (this.isMobileMenuOpen) this.toggleMobileMenu(false);
      } else {
        this.currentView = 'landing';
        if (typeof window !== 'undefined') window.location.hash = '';
        this.render();
      }
    };

    const tag = document.createElement('span');
    tag.className = 'brand-subtitle';
    tag.textContent = 'appreciation & joy';

    brandGroup.appendChild(brand);
    brandGroup.appendChild(tag);

    // 2. Desktop Nav Links
    const nav = document.createElement('nav');
    nav.className = 'landing-nav-links';
    nav.setAttribute('aria-label', 'Main Navigation');

    const sections = [
      { id: 'why-it-matters', label: 'Why Halp' },
      { id: 'showcase', label: 'The Experience' },
      { id: 'how-it-works', label: 'How It Works' },
      { id: 'security', label: 'Privacy & Care' },
    ];

    for (const sec of sections) {
      const link = document.createElement('a');
      link.href = `#${sec.id}`;
      link.textContent = sec.label;
      link.onclick = (e) => {
        e.preventDefault();
        this.navigateToSection(sec.id);
      };
      nav.appendChild(link);
    }

    // 3. Desktop Action Buttons
    const actions = document.createElement('div');
    actions.className = 'landing-nav-actions';

    // 4. Mobile Controls (Primary CTA + Hamburger Toggle)
    const mobileControls = document.createElement('div');
    mobileControls.className = 'landing-mobile-controls';

    if (this.currentKey) {
      // Desktop actions (Logged in)
      const switchBtn = document.createElement('button');
      switchBtn.type = 'button';
      switchBtn.className = 'btn btn-secondary btn-nav';
      switchBtn.textContent = 'Switch Account';
      switchBtn.onclick = () => {
        this.currentView = 'login';
        if (typeof window !== 'undefined') window.location.hash = '#login';
        this.render();
      };

      const appBtn = document.createElement('button');
      appBtn.type = 'button';
      appBtn.className = 'btn btn-nav';
      appBtn.textContent = 'Dashboard →';
      appBtn.onclick = async () => {
        try {
          await this.client.loginWithAccountKey(this.currentKey!);
          await this.loadData();
          this.currentView = 'home';
          this.initTransport();
        } catch {
          this.currentView = 'home';
        }
        if (typeof window !== 'undefined') window.location.hash = '#app';
        this.render();
      };

      actions.appendChild(switchBtn);
      actions.appendChild(appBtn);

      // Mobile Primary Action Button
      const mobileAppBtn = document.createElement('button');
      mobileAppBtn.type = 'button';
      mobileAppBtn.className = 'btn btn-nav btn-nav-mobile-primary';
      mobileAppBtn.textContent = 'Dashboard →';
      mobileAppBtn.onclick = async () => {
        try {
          await this.client.loginWithAccountKey(this.currentKey!);
          await this.loadData();
          this.currentView = 'home';
          this.initTransport();
        } catch {
          this.currentView = 'home';
        }
        if (typeof window !== 'undefined') window.location.hash = '#app';
        this.render();
      };
      mobileControls.appendChild(mobileAppBtn);
    } else {
      // Desktop actions (Unauthenticated)
      const loginBtn = document.createElement('button');
      loginBtn.type = 'button';
      loginBtn.className = 'btn btn-secondary btn-nav';
      loginBtn.textContent = 'Log In';
      loginBtn.onclick = () => {
        this.currentView = 'login';
        if (typeof window !== 'undefined') window.location.hash = '#login';
        this.render();
      };

      const registerBtn = document.createElement('button');
      registerBtn.type = 'button';
      registerBtn.className = 'btn btn-nav';
      registerBtn.textContent = 'Get Started';
      registerBtn.onclick = () => {
        this.currentKey = generateAccountKey();
        this.currentView = 'key_wall';
        if (typeof window !== 'undefined') window.location.hash = '#register';
        this.render();
      };

      actions.appendChild(loginBtn);
      actions.appendChild(registerBtn);

      // Mobile Primary Action Button
      const mobileRegisterBtn = document.createElement('button');
      mobileRegisterBtn.type = 'button';
      mobileRegisterBtn.className = 'btn btn-nav btn-nav-mobile-primary';
      mobileRegisterBtn.textContent = 'Get Started';
      mobileRegisterBtn.onclick = () => {
        this.currentKey = generateAccountKey();
        this.currentView = 'key_wall';
        if (typeof window !== 'undefined') window.location.hash = '#register';
        this.render();
      };
      mobileControls.appendChild(mobileRegisterBtn);
    }

    // Hamburger button
    const toggleBtn = document.createElement('button');
    toggleBtn.type = 'button';
    toggleBtn.id = 'mobile-menu-toggle';
    toggleBtn.className = 'btn-icon mobile-menu-toggle';
    toggleBtn.setAttribute('aria-label', 'Toggle navigation menu');
    toggleBtn.setAttribute('aria-expanded', this.isMobileMenuOpen ? 'true' : 'false');
    toggleBtn.setAttribute('aria-controls', 'landing-mobile-menu');
    toggleBtn.innerHTML = this.isMobileMenuOpen
      ? `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="18" y1="6" x2="6" y2="18"></line><line x1="6" y1="6" x2="18" y2="18"></line></svg>`
      : `<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.5" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><line x1="3" y1="6" x2="21" y2="6"></line><line x1="3" y1="12" x2="21" y2="12"></line><line x1="3" y1="18" x2="21" y2="18"></line></svg>`;

    toggleBtn.onclick = () => {
      this.toggleMobileMenu();
    };

    mobileControls.appendChild(toggleBtn);

    headerBar.appendChild(brandGroup);
    headerBar.appendChild(nav);
    headerBar.appendChild(actions);
    headerBar.appendChild(mobileControls);
    header.appendChild(headerBar);

    // 5. Mobile Drawer Menu
    const mobileMenu = document.createElement('div');
    mobileMenu.id = 'landing-mobile-menu';
    mobileMenu.className = this.isMobileMenuOpen ? 'landing-mobile-menu open' : 'landing-mobile-menu';
    mobileMenu.setAttribute('aria-hidden', this.isMobileMenuOpen ? 'false' : 'true');

    const mobileNavList = document.createElement('nav');
    mobileNavList.className = 'mobile-nav-list';
    mobileNavList.setAttribute('aria-label', 'Mobile Navigation');

    for (const sec of sections) {
      const link = document.createElement('a');
      link.href = `#${sec.id}`;
      link.className = 'mobile-nav-link';
      link.innerHTML = `<span>${sec.label}</span><span class="mobile-nav-arrow" aria-hidden="true">›</span>`;
      link.onclick = (e) => {
        e.preventDefault();
        this.navigateToSection(sec.id);
      };
      mobileNavList.appendChild(link);
    }

    const divider = document.createElement('div');
    divider.className = 'mobile-menu-divider';

    const mobileActions = document.createElement('div');
    mobileActions.className = 'mobile-menu-actions';

    if (this.currentKey) {
      const switchBtn = document.createElement('button');
      switchBtn.type = 'button';
      switchBtn.className = 'btn btn-secondary mobile-action-btn';
      switchBtn.textContent = 'Switch Account';
      switchBtn.onclick = () => {
        this.toggleMobileMenu(false);
        this.currentView = 'login';
        if (typeof window !== 'undefined') window.location.hash = '#login';
        this.render();
      };
      mobileActions.appendChild(switchBtn);
    } else {
      const loginBtn = document.createElement('button');
      loginBtn.type = 'button';
      loginBtn.className = 'btn btn-secondary mobile-action-btn';
      loginBtn.textContent = 'Log In with Key';
      loginBtn.onclick = () => {
        this.toggleMobileMenu(false);
        this.currentView = 'login';
        if (typeof window !== 'undefined') window.location.hash = '#login';
        this.render();
      };
      mobileActions.appendChild(loginBtn);
    }

    mobileMenu.appendChild(mobileNavList);
    mobileMenu.appendChild(divider);
    mobileMenu.appendChild(mobileActions);
    header.appendChild(mobileMenu);

    return header;
  }

  renderHero(): HTMLElement {
    const hero = document.createElement('section');
    hero.className = 'hero-section';

    const badges = document.createElement('div');
    badges.className = 'hero-badges';
    for (const pill of COPY.landing.pills) {
      const span = document.createElement('span');
      span.className = 'badge-pill';
      span.textContent = pill;
      badges.appendChild(span);
    }

    const title = document.createElement('h1');
    title.className = 'hero-title';
    title.textContent = COPY.landing.heroTitle;

    const subtitle = document.createElement('p');
    subtitle.className = 'hero-subtitle';
    subtitle.textContent = COPY.landing.heroSubtitle;

    const ctaGroup = document.createElement('div');
    ctaGroup.className = 'hero-cta-group';

    if (this.currentKey) {
      const appBtn = document.createElement('button');
      appBtn.type = 'button';
      appBtn.className = 'btn btn-hero';
      appBtn.textContent = 'Go to Your Dashboard →';
      appBtn.onclick = async () => {
        try {
          await this.client.loginWithAccountKey(this.currentKey!);
          await this.loadData();
          this.currentView = 'home';
          this.initTransport();
        } catch {
          this.currentView = 'home';
        }
        if (typeof window !== 'undefined') window.location.hash = '#app';
        this.render();
      };

      const loginBtn = document.createElement('button');
      loginBtn.type = 'button';
      loginBtn.className = 'btn btn-secondary btn-hero-secondary';
      loginBtn.textContent = 'Log In with Another Key';
      loginBtn.onclick = () => {
        this.currentView = 'login';
        if (typeof window !== 'undefined') window.location.hash = '#login';
        this.render();
      };

      ctaGroup.appendChild(appBtn);
      ctaGroup.appendChild(loginBtn);
    } else {
      const getStartedBtn = document.createElement('button');
      getStartedBtn.type = 'button';
      getStartedBtn.className = 'btn btn-hero';
      getStartedBtn.textContent = 'Start Spreading Appreciation';
      getStartedBtn.onclick = () => {
        this.currentKey = generateAccountKey();
        this.currentView = 'key_wall';
        if (typeof window !== 'undefined') window.location.hash = '#register';
        this.render();
      };

      const loginBtn = document.createElement('button');
      loginBtn.type = 'button';
      loginBtn.className = 'btn btn-secondary btn-hero-secondary';
      loginBtn.textContent = 'Log In with Account Key';
      loginBtn.onclick = () => {
        this.currentView = 'login';
        if (typeof window !== 'undefined') window.location.hash = '#login';
        this.render();
      };

      ctaGroup.appendChild(getStartedBtn);
      ctaGroup.appendChild(loginBtn);
    }

    const heroVisual = document.createElement('div');
    heroVisual.className = 'hero-visual-card';

    const heroImg = document.createElement('img');
    heroImg.src = './assets/halp-warm-gratitude.jpg';
    heroImg.alt = 'A peaceful, cozy moment of quiet gratitude and warmth';
    heroImg.className = 'hero-asset-img';

    const heroVisualCaption = document.createElement('div');
    heroVisualCaption.className = 'hero-visual-caption';
    heroVisualCaption.textContent = 'A quiet spark of appreciation. No unsolicited comments, no reply obligations — just pure, honest recognition that brightens someone\'s day.';

    heroVisual.appendChild(heroImg);
    heroVisual.appendChild(heroVisualCaption);

    hero.appendChild(badges);
    hero.appendChild(title);
    hero.appendChild(subtitle);
    hero.appendChild(ctaGroup);
    hero.appendChild(heroVisual);

    return hero;
  }

  renderWhyAppreciationMatters(): HTMLElement {
    const section = document.createElement('section');
    section.id = 'why-it-matters';
    section.className = 'why-matters-section';

    const header = document.createElement('div');
    header.className = 'section-header';

    const title = document.createElement('h2');
    title.textContent = 'Spreading Positive Energy at Work and Beyond';

    const desc = document.createElement('p');
    desc.className = 'caption';
    desc.textContent = 'Modern work can be busy, stressful, and isolating. A quiet moment of genuine appreciation changes everything.';

    header.appendChild(title);
    header.appendChild(desc);
    section.appendChild(header);

    const grid = document.createElement('div');
    grid.className = 'matters-grid';

    const cards = [
      {
        badge: 'Workmates & Remote Teams',
        title: 'Lift up colleagues effortlessly',
        desc: 'Notice a teammate who stayed late to fix a critical bug, led a great sprint, or quietly supported someone? Send them a quick appreciation ping. No meeting required, no performative flattery, no social debt — just pure, honest recognition that brightens their day.',
      },
      {
        badge: 'Creators & Communities',
        title: 'Say thank you across the world',
        desc: 'Open source maintainers, indie creators, educators, and community volunteers give endless hours for free. A simple appreciation ping on their profile tells them their hard work made a genuine difference to someone, somewhere in the world.',
      },
      {
        badge: 'Pure Positivity',
        title: 'Zero reply pressure, zero obligations',
        desc: 'Traditional messages demand answers, emoji replies, and endless small talk. Halp creates a peaceful space where appreciation is freely given and received with a smile, without placing any burden or social debt on the recipient.',
      },
    ];

    for (const c of cards) {
      const card = document.createElement('div');
      card.className = 'card matter-card';

      const badge = document.createElement('span');
      badge.className = 'matter-badge';
      badge.textContent = c.badge;

      const h3 = document.createElement('h3');
      h3.textContent = c.title;

      const p = document.createElement('p');
      p.className = 'caption';
      p.textContent = c.desc;

      card.appendChild(badge);
      card.appendChild(h3);
      card.appendChild(p);
      grid.appendChild(card);
    }

    section.appendChild(grid);
    return section;
  }

  renderAppShowcase(): HTMLElement {
    const section = document.createElement('section');
    section.id = 'showcase';
    section.className = 'showcase-section';

    const header = document.createElement('div');
    header.className = 'section-header';

    const title = document.createElement('h2');
    title.textContent = 'The Joy of Simple Appreciation';

    const desc = document.createElement('p');
    desc.className = 'caption';
    desc.textContent = 'Explore how Halp delivers quiet moments of happiness across teams, workspaces, and communities worldwide.';

    header.appendChild(title);
    header.appendChild(desc);
    section.appendChild(header);

    const tabsContainer = document.createElement('div');
    tabsContainer.className = 'showcase-tabs';
    tabsContainer.setAttribute('role', 'tablist');

    const tabDefs = [
      { id: 0, label: 'Arrival of Happiness (Home)' },
      { id: 1, label: 'Send a Spark of Joy' },
      { id: 2, label: 'Share Kindness Anywhere' },
      { id: 3, label: 'Personal Key Wall' },
      { id: 4, label: 'Gentle Delivery Settings' },
    ];

    for (const tab of tabDefs) {
      const btn = document.createElement('button');
      btn.type = 'button';
      btn.className = `tab-btn ${this.activePreviewTab === tab.id ? 'active' : ''}`;
      btn.textContent = tab.label;
      btn.onclick = () => {
        this.activePreviewTab = tab.id;
        this.render();
        const el = document.getElementById('showcase');
        if (el && el.scrollIntoView) {
          el.scrollIntoView({ behavior: 'auto' });
        }
      };
      tabsContainer.appendChild(btn);
    }
    section.appendChild(tabsContainer);

    const windowFrame = document.createElement('div');
    windowFrame.className = 'mockup-window';

    const winHeader = document.createElement('div');
    winHeader.className = 'mockup-header';

    const dots = document.createElement('div');
    dots.className = 'mockup-dots';
    dots.innerHTML = '<span class="dot dot-red"></span><span class="dot dot-yellow"></span><span class="dot dot-green"></span>';

    const urlBar = document.createElement('div');
    urlBar.className = 'mockup-url';
    const urls = [
      'https://halp.to/app (Home Dashboard)',
      'https://halp.to/h/e7k4p2m9qx3va (Recipient Page)',
      'https://halp.to/share/e7k4p2m9qx3va (QR Generator)',
      'https://halp.to/account/key (Key Wall)',
      'https://halp.to/settings (Delivery Controls)',
    ];
    urlBar.textContent = urls[this.activePreviewTab] || 'https://halp.to';

    winHeader.appendChild(dots);
    winHeader.appendChild(urlBar);
    windowFrame.appendChild(winHeader);

    const winBody = document.createElement('div');
    winBody.className = 'mockup-body';

    if (this.activePreviewTab === 0) {
      winBody.appendChild(this.renderMockHome());
    } else if (this.activePreviewTab === 1) {
      winBody.appendChild(this.renderMockPing());
    } else if (this.activePreviewTab === 2) {
      winBody.appendChild(this.renderMockShare());
    } else if (this.activePreviewTab === 3) {
      winBody.appendChild(this.renderMockKeyWall());
    } else {
      winBody.appendChild(this.renderMockSettings());
    }

    windowFrame.appendChild(winBody);
    section.appendChild(windowFrame);

    const explainerTexts = [
      'The Home Dashboard shows the quiet arrival of appreciation. No unread counts, no guilt, no pressure to respond.',
      'The Recipient Page is pure simplicity: one tap to send an anonymous spark of joy to a colleague or creator.',
      'Effortless sharing: place your private link or QR code anywhere colleagues, contributors, or followers can find it.',
      'Zero passwords, zero data harvesting: your 16-digit client key keeps you in complete control without surrendering personal data.',
      'Gentle delivery settings: batch arrivals into hourly or daily digests, or pause whenever you need uninterrupted focus.',
    ];

    const explainer = document.createElement('div');
    explainer.className = 'mock-explainer';
    explainer.innerHTML = `<strong>Experience Highlight:</strong> ${explainerTexts[this.activePreviewTab] || ''}`;
    section.appendChild(explainer);

    return section;
  }

  renderMockHome(): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'mockup-content';

    const countCard = document.createElement('div');
    countCard.className = 'card';
    const countTitle = document.createElement('h1');
    countTitle.textContent = '7 people appreciate you.';
    countCard.appendChild(countTitle);

    const sendCard = document.createElement('div');
    sendCard.className = 'card';
    const sendTitle = document.createElement('h2');
    sendTitle.textContent = 'Send appreciation';

    const form = document.createElement('div');
    form.className = 'form-group';
    const input = document.createElement('input');
    input.type = 'text';
    input.value = 'e7k4p2m9qx3va';
    input.readOnly = true;

    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn';
    btn.textContent = 'Appreciate them';
    btn.onclick = () => {
      btn.disabled = true;
      btn.textContent = 'Sending...';
      setTimeout(() => {
        btn.textContent = 'Sent.';
        setTimeout(() => {
          btn.disabled = false;
          btn.textContent = 'Appreciate them';
        }, 2000);
      }, 300);
    };

    form.appendChild(input);
    form.appendChild(btn);
    sendCard.appendChild(sendTitle);
    sendCard.appendChild(form);

    const handlesCard = document.createElement('div');
    handlesCard.className = 'card';
    const handlesTitle = document.createElement('h2');
    handlesTitle.textContent = 'Your Handles';
    handlesCard.appendChild(handlesTitle);

    const handleRow = document.createElement('div');
    handleRow.className = 'handle-item';
    handleRow.innerHTML = `
      <div class="handle-item-header">
        <span class="handle-tag">e7k4p2m9qx3va</span>
        <span class="handle-kind">personal</span>
      </div>
      <div style="font-size:0.9375rem;">GitHub Profile & Bio</div>
      <div class="button-group">
        <button type="button" class="btn btn-secondary">Share</button>
        <button type="button" class="btn btn-secondary">Pause</button>
      </div>
    `;
    handlesCard.appendChild(handleRow);

    wrap.appendChild(countCard);
    wrap.appendChild(sendCard);
    wrap.appendChild(handlesCard);
    return wrap;
  }

  renderMockPing(): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'mockup-content mockup-center';

    const card = document.createElement('div');
    card.className = 'card mock-ping-card';

    const h1 = document.createElement('h1');
    h1.textContent = 'Someone appreciates you.';

    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn';
    btn.textContent = 'Appreciate them';
    btn.onclick = () => {
      btn.disabled = true;
      btn.textContent = 'Sending...';
      setTimeout(() => {
        btn.textContent = 'Sent.';
        setTimeout(() => {
          btn.disabled = false;
          btn.textContent = 'Appreciate them';
        }, 2000);
      }, 300);
    };

    const p = document.createElement('p');
    p.className = 'caption';
    p.textContent = 'Anonymous. No message. They never learn it was you.';

    card.appendChild(h1);
    card.appendChild(btn);
    card.appendChild(p);
    wrap.appendChild(card);
    return wrap;
  }

  renderMockShare(): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'mockup-content mockup-center';

    const card = document.createElement('div');
    card.className = 'card';

    const h2 = document.createElement('h2');
    h2.textContent = 'Share e7k4p2m9qx3va';

    const link = 'https://halp.to/h/e7k4p2m9qx3va';
    const linkDiv = document.createElement('div');
    linkDiv.className = 'mock-link-preview';
    linkDiv.textContent = link;

    const qr = new QRCode(link);
    const qrDiv = document.createElement('div');
    qrDiv.style.display = 'flex';
    qrDiv.style.justifyContent = 'center';
    qrDiv.style.padding = '0.75rem';
    qrDiv.innerHTML = qr.toSVG(4);
    qrDiv.querySelector('svg')?.setAttribute('style', 'max-width: 160px; width: 100%; height: auto;');

    const actions = document.createElement('div');
    actions.className = 'button-group';
    actions.innerHTML = `
      <button type="button" class="btn btn-secondary">Copy Link</button>
      <button type="button" class="btn btn-secondary">Download QR PNG</button>
      <button type="button" class="btn btn-secondary">Copy Badge Markdown</button>
    `;

    card.appendChild(h2);
    card.appendChild(linkDiv);
    card.appendChild(qrDiv);
    card.appendChild(actions);
    wrap.appendChild(card);
    return wrap;
  }

  renderMockKeyWall(): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'mockup-content mockup-center';

    const card = document.createElement('div');
    card.className = 'card';

    const digitsDiv = document.createElement('div');
    digitsDiv.className = 'key-wall-digits';
    digitsDiv.textContent = '6421  8830  5197  4462';

    const btnGroup = document.createElement('div');
    btnGroup.className = 'button-group';
    btnGroup.innerHTML = `
      <button type="button" class="btn btn-secondary">Copy</button>
      <button type="button" class="btn btn-secondary">Download as text</button>
    `;

    const titleP = document.createElement('p');
    titleP.style.fontWeight = '600';
    titleP.textContent = COPY.keyWall.title;

    const subP = document.createElement('p');
    subP.className = 'caption';
    subP.textContent = COPY.keyWall.subtitle;

    const checkLabel = document.createElement('label');
    checkLabel.className = 'checkbox-group';
    checkLabel.innerHTML = `
      <input type="checkbox" checked disabled>
      <span>${COPY.keyWall.checkbox}</span>
    `;

    const contBtn = document.createElement('button');
    contBtn.type = 'button';
    contBtn.className = 'btn';
    contBtn.textContent = 'Continue';

    card.appendChild(digitsDiv);
    card.appendChild(btnGroup);
    card.appendChild(titleP);
    card.appendChild(subP);
    card.appendChild(checkLabel);
    card.appendChild(contBtn);
    wrap.appendChild(card);
    return wrap;
  }

  renderMockSettings(): HTMLElement {
    const wrap = document.createElement('div');
    wrap.className = 'mockup-content';

    const card = document.createElement('div');
    card.className = 'card';

    const h1 = document.createElement('h1');
    h1.textContent = 'Settings';

    const disclaimer = document.createElement('div');
    disclaimer.className = 'notice-box';
    disclaimer.textContent = COPY.settings.escalationDisclaimer;

    const settingsList = document.createElement('div');
    settingsList.className = 'mock-settings-list';
    settingsList.innerHTML = `
      <div class="mock-setting-row">
        <div><strong>Digest Window</strong><div class="caption">Batching arrival notifications</div></div>
        <div class="preset-pill-group">
          <span class="preset-pill">Instant</span>
          <span class="preset-pill">Every minute</span>
          <span class="preset-pill active">Hourly</span>
          <span class="preset-pill">Daily</span>
        </div>
      </div>
      <div class="mock-setting-row">
        <div><strong>Delivery Mode</strong><div class="caption">Notification filtering</div></div>
        <div class="preset-pill-group">
          <span class="preset-pill active">Everything</span>
          <span class="preset-pill">Groups only</span>
          <span class="preset-pill">Paused</span>
        </div>
      </div>
      <div class="mock-setting-row">
        <div><strong>Max Notifications</strong><div class="caption">Rate limiter threshold</div></div>
        <div><strong>12 per hour</strong></div>
      </div>
    `;

    card.appendChild(h1);
    card.appendChild(disclaimer);
    card.appendChild(settingsList);
    wrap.appendChild(card);
    return wrap;
  }

  renderHowItWorks(): HTMLElement {
    const section = document.createElement('section');
    section.id = 'how-it-works';
    section.className = 'how-it-works-section';

    const header = document.createElement('div');
    header.className = 'section-header';

    const title = document.createElement('h2');
    title.textContent = 'How Simple Appreciation Works';

    const desc = document.createElement('p');
    desc.className = 'caption';
    desc.textContent = 'Three easy steps to spread positive energy and brighten someone\'s day.';

    header.appendChild(title);
    header.appendChild(desc);
    section.appendChild(header);

    const grid = document.createElement('div');
    grid.className = 'steps-grid';

    const steps = [
      {
        num: '01',
        title: 'Get your personal link',
        desc: 'Generate your private appreciation handle in one click with your 16-digit client key. No email, password, or setup hassle required.',
      },
      {
        num: '02',
        title: 'Share with teammates & community',
        desc: 'Drop your link or SVG badge into your Slack bio, GitHub README, PR template, or email signature for colleagues and supporters.',
      },
      {
        num: '03',
        title: 'Spread & receive genuine joy',
        desc: 'Send a quiet spark of gratitude to teammates who helped you out. Receive arrival counts silently without any obligation to reply or make small talk.',
      },
    ];

    for (const s of steps) {
      const card = document.createElement('div');
      card.className = 'card step-card';

      const numSpan = document.createElement('span');
      numSpan.className = 'step-num';
      numSpan.textContent = s.num;

      const stepTitle = document.createElement('h3');
      stepTitle.textContent = s.title;

      const stepDesc = document.createElement('p');
      stepDesc.className = 'caption';
      stepDesc.textContent = s.desc;

      card.appendChild(numSpan);
      card.appendChild(stepTitle);
      card.appendChild(stepDesc);
      grid.appendChild(card);
    }

    section.appendChild(grid);
    return section;
  }

  renderSafeAndRespectful(): HTMLElement {
    const section = document.createElement('section');
    section.id = 'security';
    section.className = 'security-section';

    const header = document.createElement('div');
    header.className = 'section-header';

    const title = document.createElement('h2');
    title.textContent = 'Kindness Protected by Design';

    const desc = document.createElement('p');
    desc.className = 'caption';
    desc.textContent = 'Halp is engineered so appreciation remains pure, joyful, and completely free from noise, pressure, and toxicity.';

    header.appendChild(title);
    header.appendChild(desc);
    section.appendChild(header);

    const visual = document.createElement('div');
    visual.className = 'security-visual-card';

    const visualImg = document.createElement('img');
    visualImg.src = './assets/halp-caring-light.jpg';
    visualImg.alt = 'A gentle lantern glowing softly with kindness and peace of mind';
    visualImg.className = 'security-asset-img';

    const visualCaption = document.createElement('div');
    visualCaption.className = 'security-visual-caption';
    visualCaption.textContent = 'Protected kindness: a calm, quiet space free from public scrutiny, ads, tracking, and notification noise.';

    visual.appendChild(visualImg);
    visual.appendChild(visualCaption);
    section.appendChild(visual);

    const grid = document.createElement('div');
    grid.className = 'security-grid';

    const items = [
      {
        title: 'Pure Appreciation Only',
        desc: 'No message fields, no text comments, and no downvotes. By design, the only thing anyone can ever send is "someone appreciates you".',
      },
      {
        title: 'Zero Reply Pressure',
        desc: 'Traditional messages demand small talk and replies. Halp removes the obligation — recipients feel appreciated without any social debt.',
      },
      {
        title: 'Honest & Free of Politics',
        desc: 'Senders are completely anonymous. Colleagues and peers can express genuine gratitude freely without feeling performative or awkward.',
      },
      {
        title: 'Calm & In Control',
        desc: 'Digest windows, hourly limits, and one-click pausing ensure appreciation brings calm happiness rather than notification fatigue.',
      },
    ];

    for (const item of items) {
      const card = document.createElement('div');
      card.className = 'card security-card';

      const itemTitle = document.createElement('h3');
      itemTitle.textContent = item.title;

      const itemDesc = document.createElement('p');
      itemDesc.className = 'caption';
      itemDesc.textContent = item.desc;

      card.appendChild(itemTitle);
      card.appendChild(itemDesc);
      grid.appendChild(card);
    }

    section.appendChild(grid);
    return section;
  }

  renderQuickAccess(): HTMLElement {
    const section = document.createElement('section');
    section.className = 'quick-access-section';

    const card = document.createElement('div');
    card.className = 'card quick-access-card';

    const leftCol = document.createElement('div');
    leftCol.className = 'quick-col';

    const newH3 = document.createElement('h3');
    newH3.textContent = 'Spread Joy to Someone Today';

    const newP = document.createElement('p');
    newP.className = 'caption';
    newP.textContent = 'Create your private appreciation link in seconds. No email, no password, and no spam.';

    const newBtn = document.createElement('button');
    newBtn.type = 'button';
    newBtn.className = 'btn';
    newBtn.textContent = 'Create Your Link';
    newBtn.onclick = () => {
      this.currentKey = generateAccountKey();
      this.currentView = 'key_wall';
      this.render();
    };

    leftCol.appendChild(newH3);
    leftCol.appendChild(newP);
    leftCol.appendChild(newBtn);

    const divider = document.createElement('div');
    divider.className = 'quick-divider';

    const rightCol = document.createElement('div');
    rightCol.className = 'quick-col';

    const logH3 = document.createElement('h3');
    logH3.textContent = 'Check Your Appreciation';

    const logP = document.createElement('p');
    logP.className = 'caption';
    logP.textContent = 'Enter your 16-digit key to see how many people appreciated you today and manage your handles.';

    const form = document.createElement('form');
    form.className = 'form-group';

    const input = document.createElement('input');
    input.type = 'text';
    input.maxLength = 19;
    input.placeholder = '6421 8830 5197 4462';
    input.oninput = () => {
      const clean = input.value.replace(/\D/g, '').slice(0, 16);
      input.value = formatAccountKey(clean);
    };

    const errDiv = document.createElement('div');
    errDiv.style.color = 'var(--danger)';
    errDiv.style.fontSize = '0.875rem';
    errDiv.style.display = 'none';

    const logSubmit = document.createElement('button');
    logSubmit.type = 'submit';
    logSubmit.className = 'btn btn-secondary';
    logSubmit.textContent = 'Log In with Key';

    form.onsubmit = async (e) => {
      e.preventDefault();
      errDiv.style.display = 'none';
      const clean = input.value.replace(/\s+/g, '');
      if (!isAccountKeyShaped(clean)) {
        errDiv.textContent = COPY.login.failure;
        errDiv.style.display = 'block';
        return;
      }
      logSubmit.disabled = true;
      try {
        await this.client.loginWithAccountKey(clean);
        await storeAccountKey(clean);
        this.currentKey = clean;
        await this.loadData();
        this.currentView = 'home';
        this.initTransport();
        this.render();
      } catch {
        errDiv.textContent = COPY.login.failure;
        errDiv.style.display = 'block';
        logSubmit.disabled = false;
      }
    };

    form.appendChild(input);
    form.appendChild(errDiv);
    form.appendChild(logSubmit);

    rightCol.appendChild(logH3);
    rightCol.appendChild(logP);
    rightCol.appendChild(form);

    card.appendChild(leftCol);
    card.appendChild(divider);
    card.appendChild(rightCol);
    section.appendChild(card);

    return section;
  }

  renderLandingFooter(): HTMLElement {
    const footer = document.createElement('footer');
    footer.className = 'landing-footer';

    const topDiv = document.createElement('div');
    topDiv.className = 'footer-top';

    const brandDiv = document.createElement('div');
    brandDiv.innerHTML = '<strong>halp</strong> — Someone appreciates you.';

    const linksDiv = document.createElement('div');
    linksDiv.className = 'footer-links';
    linksDiv.innerHTML = `
      <a href="https://halp.to/github">GitHub</a>
      <a href="./docs/API.md">API</a>
      <a href="./docs/ARCHITECTURE.md">Architecture</a>
      <a href="./docs/PRIVACY.md">Privacy Policy</a>
    `;

    topDiv.appendChild(brandDiv);
    topDiv.appendChild(linksDiv);

    const bottomDiv = document.createElement('div');
    bottomDiv.className = 'footer-bottom caption';
    bottomDiv.textContent = 'Open source under Apache-2.0. Single EU region (eu-1). Zero trackers, zero cookies.';

    footer.appendChild(topDiv);
    footer.appendChild(bottomDiv);
    return footer;
  }

  renderHeader(): HTMLElement {
    const header = document.createElement('header');
    header.className = 'app-header';

    const brand = document.createElement('a');
    brand.href = '#';
    brand.className = 'brand brand-link';
    brand.innerHTML = '<span class="brand-spark">💛</span> <span class="brand-text">halp</span>';
    brand.style.cursor = 'pointer';
    brand.title = 'Back to Landing Page';
    brand.onclick = (e) => {
      e.preventDefault();
      this.currentView = 'landing';
      if (typeof window !== 'undefined') window.location.hash = '';
      this.render();
    };

    const nav = document.createElement('nav');
    nav.className = 'nav-links app-nav-links';
    nav.setAttribute('aria-label', 'App Navigation');

    const landingLink = document.createElement('a');
    landingLink.href = '#';
    landingLink.textContent = 'Overview';
    if (this.currentView === 'landing') landingLink.className = 'active';
    landingLink.onclick = (e) => {
      e.preventDefault();
      this.currentView = 'landing';
      if (typeof window !== 'undefined') window.location.hash = '';
      this.render();
    };

    const homeLink = document.createElement('a');
    homeLink.href = '#app';
    homeLink.textContent = 'Home';
    if (this.currentView === 'home') homeLink.className = 'active';
    homeLink.onclick = (e) => {
      e.preventDefault();
      this.currentView = 'home';
      if (typeof window !== 'undefined') window.location.hash = '#app';
      this.render();
    };

    const settingsLink = document.createElement('a');
    settingsLink.href = '#settings';
    settingsLink.textContent = 'Settings';
    if (this.currentView === 'settings') settingsLink.className = 'active';
    settingsLink.onclick = (e) => {
      e.preventDefault();
      this.currentView = 'settings';
      this.render();
    };

    nav.appendChild(landingLink);
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

    const backBtn = document.createElement('button');
    backBtn.type = 'button';
    backBtn.className = 'btn btn-secondary';
    backBtn.textContent = '← Back to overview';
    backBtn.style.alignSelf = 'flex-start';
    backBtn.onclick = () => {
      this.currentView = 'landing';
      this.render();
    };

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

    const backBtn = document.createElement('button');
    backBtn.type = 'button';
    backBtn.className = 'btn btn-secondary';
    backBtn.textContent = '← Back to overview';
    backBtn.style.alignSelf = 'flex-start';
    backBtn.onclick = () => {
      this.currentView = 'landing';
      this.render();
    };

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

    // Account Actions
    const accountActions = document.createElement('div');
    accountActions.className = 'button-group';
    accountActions.style.marginTop = '1rem';
    accountActions.style.borderTop = '1px solid var(--border)';
    accountActions.style.paddingTop = '1rem';

    const viewKeyBtn = document.createElement('button');
    viewKeyBtn.type = 'button';
    viewKeyBtn.className = 'btn btn-secondary';
    viewKeyBtn.textContent = 'View Account Key';
    viewKeyBtn.onclick = () => {
      this.currentView = 'key_wall';
      this.render();
    };

    const logoutBtn = document.createElement('button');
    logoutBtn.type = 'button';
    logoutBtn.className = 'btn btn-secondary';
    logoutBtn.textContent = 'Log out (Disconnect key)';
    logoutBtn.onclick = async () => {
      await clearAccountKey();
      this.currentKey = null;
      if (this.transport) {
        this.transport.close();
        this.transport = null;
      }
      this.currentView = 'landing';
      this.render();
    };

    accountActions.appendChild(viewKeyBtn);
    accountActions.appendChild(logoutBtn);
    card.appendChild(accountActions);

    return card;
  }
}
