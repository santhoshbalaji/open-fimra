import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;

import 'api/api_client.dart';
import 'auth/oidc.dart';
import 'auth/session.dart';
import 'auth/tokens.dart';
import 'config.dart';
import 'data/alert_repository.dart';
import 'data/field_store.dart';
import 'data/offline_repository.dart';
import 'device/device_manager.dart';
import 'device/device_store.dart';
import 'device/local_data.dart';
import 'device/push.dart';
import 'device/push_platform.dart';
import 'forms/report_media_cache.dart';
import 'theme/app_theme.dart';
import 'ui/home.dart';
import 'ui/sign_in.dart';
import 'ui/widgets.dart';

/// Everything that depends on the server settings. A new set is made when
/// the user changes them.
class AppServices {
  AppServices({required this.config, required this.session, required this.repository, this.devices});

  /// Connects to the servers in [config] over [httpClient].
  factory AppServices.connect(
    AppConfig config, {
    http.Client? httpClient,
    TokenStore? tokenStore,
    DeviceStore? deviceStore,
    PushService? push,
    AlertNotifier? notifier,
    AuthBrowser browser = const SystemAuthBrowser(),
  }) {
    final client = httpClient ?? http.Client();
    final tokens = tokenStore ?? SecureTokenStore();
    final ids = deviceStore ?? SecureDeviceStore();
    final pushService = push ?? platformPush();
    final alerts = notifier ?? platformNotifier();
    final session = Session(
      oidc: OidcClient(config: config, httpClient: client),
      store: tokens,
      browser: browser,
    );
    final field = platformFieldStore();
    late final DeviceManager devices;
    final api = FimraApi(
      baseUrl: config.apiBaseUrl,
      token: ({bool forceRefresh = false}) => session.accessToken(forceRefresh: forceRefresh),
      httpClient: client,
      deviceId: () => devices.deviceId,
      onDeviceAction: (action, message) => devices.handleAction(action, message),
    );
    devices = DeviceManager(
      api: api,
      session: session,
      store: ids,
      push: pushService,
      notifier: alerts,
      localData: fieldDataOnPhone(
        notifications: alerts.clear,
        pushRegistration: pushService.stop,
        offlineMaps: clearMapRegions,
        offlineReports: field?.wipe,
        mediaCache: field == null ? null : clearReportMediaCache,
        deviceId: ids.clear,
        signInTokens: tokens.clear,
      ),
    );
    return AppServices(
      config: config,
      session: session,
      repository: field == null
          ? OnlineAlertRepository(api)
          : OfflineAlertRepository(api, field, () => devices.endingSession ? null : session.subject),
      devices: devices,
    );
  }

  final AppConfig config;
  final Session session;
  final AlertRepository repository;
  OfflineAlertRepository? get offline =>
      repository is OfflineAlertRepository ? repository as OfflineAlertRepository : null;

  /// This phone's registration, push notifications and wipe handling.
  final DeviceManager? devices;

  /// Signs out on the phone, the API and the sign-in server.
  Future<void> signOut() async {
    await (devices?.signOut() ?? session.signOut());
    await offline?.store.wipe();
  }

  /// Ends a session the server no longer accepts.
  Future<void> sessionEnded() =>
      devices?.sessionEnded() ?? session.signOut(message: 'Your session has ended. Sign in again.');

  /// Starts the session and the phone's registration.
  Future<void> start({bool foreground = true}) async {
    await devices?.start(foreground: foreground);
    await session.restore();
  }

  void dispose() {
    offline?.dispose();
    devices?.dispose();
  }
}

typedef ServicesFactory = AppServices Function(AppConfig config);

class FimraApp extends StatefulWidget {
  const FimraApp({
    super.key,
    required this.configStore,
    required this.initialConfig,
    required this.servicesFactory,
    this.mapBuilder,
    this.themeMode = ThemeMode.system,
  });

  final ConfigStore configStore;
  final AppConfig initialConfig;
  final ServicesFactory servicesFactory;

  /// Replaces the map, for widget tests that have no map view.
  final MapBuilder? mapBuilder;
  final ThemeMode themeMode;

  @override
  State<FimraApp> createState() => _FimraAppState();
}

class _FimraAppState extends State<FimraApp> {
  late AppServices _services = widget.servicesFactory(widget.initialConfig);

  @override
  void initState() {
    super.initState();
    _services.start();
  }

  @override
  void dispose() {
    _services.dispose();
    super.dispose();
  }

  Future<void> _changeConfig(AppConfig config, {bool reset = false}) async {
    if (reset) {
      await widget.configStore.reset();
    } else {
      await widget.configStore.save(config);
    }
    final next = reset ? await widget.configStore.load() : config;
    final previous = _services;
    await previous.signOut();
    setState(() => _services = widget.servicesFactory(next));
    WidgetsBinding.instance.addPostFrameCallback((_) => previous.dispose());
    await _services.start();
  }

  @override
  Widget build(BuildContext context) => ListenableBuilder(
    listenable: _services.session,
    builder: (context, _) {
      final session = _services.session;
      return MaterialApp(
        // Discard any open alert, map or form when the session is cleared.
        key: ValueKey(session.status == SessionStatus.signedIn),
        title: 'Open FIMRA',
        debugShowCheckedModeBanner: false,
        theme: fimraTheme(Brightness.light),
        darkTheme: fimraTheme(Brightness.dark),
        themeMode: widget.themeMode,
        home: switch (session.status) {
          SessionStatus.starting => const Scaffold(body: Center(child: FimraLogo(height: 64))),
          SessionStatus.signedOut || SessionStatus.signingIn => SignInScreen(
            session: session,
            config: _services.config,
            defaults: widget.configStore.defaults,
            onConfigChanged: _changeConfig,
          ),
          SessionStatus.signedIn => HomeScreen(
            key: ValueKey(_services),
            services: _services,
            mapBuilder: widget.mapBuilder,
          ),
        },
      );
    },
  );
}
