/**
 * Root: zero-onboarding first screen (scan a company QR code), contacts
 * list when more than one company is added, straight to the company
 * otherwise (single-source shortcut, per the product brief).
 *
 * One Expo app for every target: iOS and Android run it natively, the web
 * target is the installable PWA (Expo web + the custom service worker).
 */
import { useEffect, useState } from 'react';
import { BackHandler, Platform, View } from 'react-native';
import { SafeAreaProvider } from 'react-native-safe-area-context';
import { StatusBar } from 'expo-status-bar';
import * as Linking from 'expo-linking';
import { AppProvider, useApp } from './src/state';
import { AddCompany } from './src/ui/AddCompany';
import { Contacts } from './src/ui/Contacts';
import { CompanyView } from './src/ui/Company';
import { Body, Button, Screen, Spinner, Title } from './src/ui/components';
import { BuildStamp } from './src/ui/BuildStamp';
import { getAllItems, getCompany, getItems, type CompanyRecord, type StoredItem } from './src/lib/store';
import { joinUrlFromDeepLink } from './src/lib/payload';
import { spacing } from './src/theme';
import { registerServiceWorker } from './src/lib/pwa';

type Route =
  | { t: 'start' }
  | { t: 'contacts' }
  | { t: 'company'; origin: string }
  | { t: 'add'; from: 'start' | 'contacts'; repairOrigin?: string; deepLink?: string };

export default function App() {
  return (
    <SafeAreaProvider>
      <StatusBar style="auto" />
      <AppProvider>
        <Root />
      </AppProvider>
    </SafeAreaProvider>
  );
}

function Root() {
  const { companies, loaded, notification, freshTest, actions } = useApp();
  const [view, setView] = useState<Route>({ t: 'start' });
  const [contactsItems, setContactsItems] = useState<StoredItem[]>([]);

  // The web target is the installable PWA: register the custom service
  // worker (offline shell + relay wake-ups) once.
  useEffect(() => {
    if (Platform.OS === 'web') registerServiceWorker();
  }, []);

  // Android hardware back: the app's routing is component state, so without this
  // the gesture would exit the app from any screen instead of going back one
  // step (the Capacitor WebView used browser history for the same effect).
  useEffect(() => {
    if (Platform.OS !== 'android') return;
    const sub = BackHandler.addEventListener('hardwareBackPress', () => {
      if (view.t === 'add') {
        if (view.repairOrigin) setView({ t: 'company', origin: view.repairOrigin });
        else if (view.from === 'contacts' || companies.length > 1) setView({ t: 'contacts' });
        else setView({ t: 'start' });
        return true;
      }
      if (view.t === 'company' && companies.length > 1) {
        setView({ t: 'contacts' });
        return true;
      }
      return false; // start: let the system handle it (exit)
    });
    return () => sub.remove();
  }, [view, companies.length]);

  // Contacts shows unread counts; refresh them whenever the list is shown
  // or the company set changes (e.g. after a sync or Remove company).
  useEffect(() => {
    if (view.t !== 'contacts') return;
    let alive = true;
    void getAllItems().then((list) => {
      if (alive) setContactsItems(list);
    });
    return () => {
      alive = false;
    };
  }, [view.t, companies]);

  // initial routing: single company → straight to it; none → start; else contacts.
  // Also re-routes when the open company disappears (e.g. after Remove company).
  useEffect(() => {
    if (!loaded) return;
    if (view.t === 'company' && !companies.some((company) => company.origin === view.origin)) {
      if (companies.length === 1) setView({ t: 'company', origin: companies[0].origin });
      else if (companies.length > 1) setView({ t: 'contacts' });
      else setView({ t: 'start' });
      return;
    }
    if (view.t !== 'start' && view.t !== 'contacts') return;
    if (companies.length === 1) {
      setView({ t: 'company', origin: companies[0].origin });
    } else if (companies.length > 1) {
      setView({ t: 'contacts' });
    } else {
      setView({ t: 'start' });
    }
  }, [loaded, companies]);

  // A deep link (`?domain=&p=`) starts pairing immediately, then drops the
  // params so a reload does not re-trigger pairing. Both the initial URL (cold
  // start) and later `url` events (a link tapped while the app runs) are handled.
  useEffect(() => {
    const open = (url: string) => {
      let parsed: URL;
      try {
        parsed = new URL(url);
      } catch {
        return;
      }
      const domain = parsed.searchParams.get('domain');
      if (!domain) return;
      const join = joinUrlFromDeepLink(domain, parsed.searchParams.get('p'));
      if (!join) return;
      setView({ t: 'add', from: 'start', deepLink: join });
      // the web keeps the query string in the address bar: strip it so a reload
      // does not start pairing again
      if (Platform.OS === 'web' && typeof window !== 'undefined') {
        window.history.replaceState(null, '', window.location.pathname + window.location.hash);
      }
    };
    void Linking.getInitialURL().then((url) => {
      if (url) open(url);
    });
    const sub = Linking.addEventListener('url', ({ url }) => open(url));
    return () => sub.remove();
  }, []);

  if (!loaded) {
    return (
      <Screen>
        <Spinner />
      </Screen>
    );
  }

  if (view.t === 'add') {
    return (
      <AddCompany
        initialUrl={view.deepLink}
        repairOrigin={view.repairOrigin}
        onDone={(origin) => setView({ t: 'company', origin })}
        onCancel={() => {
          if (view.repairOrigin) setView({ t: 'company', origin: view.repairOrigin });
          else if (view.from === 'contacts' || companies.length > 1) setView({ t: 'contacts' });
          else setView({ t: 'start' });
        }}
      />
    );
  }

  if (view.t === 'contacts') {
    return (
      <Contacts
        companies={companies}
        items={contactsItems}
        notification={notification}
        freshTest={freshTest}
        actions={actions}
        onOpen={(origin) => setView({ t: 'company', origin })}
        onAdd={() => setView({ t: 'add', from: 'contacts' })}
      />
    );
  }

  if (view.t === 'company') {
    return (
      <CompanyRoute
        origin={view.origin}
        onBackToContacts={() => setView({ t: 'contacts' })}
        onRepair={(origin) => setView({ t: 'add', from: 'contacts', repairOrigin: origin })}
        onAddCompany={() => setView({ t: 'add', from: 'start' })}
      />
    );
  }

  return (
    <Screen>
      <View style={{ flex: 1, padding: spacing(2.5), gap: spacing(1.5) }}>
        <Title>Company messages</Title>
        <Body muted>
          Companies you follow publish here. Messages are verified, so nothing can be faked.
        </Body>
        <Button title="Add a company" onPress={() => setView({ t: 'add', from: 'start' })} />
        <Body muted style={{ marginTop: spacing(2) }}>
          This build checks for new messages while it is open; timely wake-ups need
          notifications on.
        </Body>
        <BuildStamp />
      </View>
    </Screen>
  );
}

function CompanyRoute({
  origin,
  onBackToContacts,
  onRepair,
  onAddCompany,
}: {
  origin: string;
  onBackToContacts: () => void;
  onRepair: (origin: string) => void;
  onAddCompany: () => void;
}) {
  const { companies } = useApp();
  const [company, setCompany] = useState<CompanyRecord | null>(null);
  const [companyItems, setCompanyItems] = useState<StoredItem[]>([]);

  useEffect(() => {
    let alive = true;
    void (async () => {
      const c = await getCompany(origin);
      if (alive) setCompany(c ?? null);
      const list = await getItems(origin);
      if (alive) setCompanyItems(list);
    })();
    return () => {
      alive = false;
    };
  }, [origin, companies]);

  if (!company) {
    return (
      <Screen>
        <Spinner />
      </Screen>
    );
  }

  return (
    <CompanyView
      company={company}
      items={companyItems}
      onBack={onBackToContacts}
      onRepair={onRepair}
      onAdd={onAddCompany}
    />
  );
}
