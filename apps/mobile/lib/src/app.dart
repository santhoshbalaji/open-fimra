import 'package:flutter/material.dart';
import 'package:http/http.dart' as http;

import 'api/api_client.dart';
import 'auth/oidc.dart';
import 'auth/session.dart';
import 'auth/tokens.dart';
import 'config.dart';
import 'data/alert_repository.dart';
import 'theme/app_theme.dart';
import 'ui/home.dart';
import 'ui/sign_in.dart';
import 'ui/widgets.dart';

/// Everything that depends on the server settings. A new set is made when
/// the user changes them.
class AppServices {
  AppServices({required this.config, required this.session, required this.repository});

  /// Connects to the servers in [config] over [httpClient].
  factory AppServices.connect(
    AppConfig config, {
    http.Client? httpClient,
    TokenStore? tokenStore,
    AuthBrowser browser = const SystemAuthBrowser(),
  }) {
    final client = httpClient ?? http.Client();
    final session = Session(
      oidc: OidcClient(config: config, httpClient: client),
      store: tokenStore ?? SecureTokenStore(),
      browser: browser,
    );
    final api = FimraApi(
      baseUrl: config.apiBaseUrl,
      token: ({bool forceRefresh = false}) => session.accessToken(forceRefresh: forceRefresh),
      httpClient: client,
    );
    return AppServices(config: config, session: session, repository: OnlineAlertRepository(api));
  }

  final AppConfig config;
  final Session session;
  final AlertRepository repository;
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
    _services.session.restore();
  }

  Future<void> _changeConfig(AppConfig config, {bool reset = false}) async {
    if (reset) {
      await widget.configStore.reset();
    } else {
      await widget.configStore.save(config);
    }
    final next = reset ? await widget.configStore.load() : config;
    await _services.session.signOut();
    setState(() => _services = widget.servicesFactory(next));
    await _services.session.restore();
  }

  @override
  Widget build(BuildContext context) => MaterialApp(
    title: 'Open FIMRA',
    debugShowCheckedModeBanner: false,
    theme: fimraTheme(Brightness.light),
    darkTheme: fimraTheme(Brightness.dark),
    themeMode: widget.themeMode,
    home: ListenableBuilder(
      listenable: _services.session,
      builder: (context, _) {
        final session = _services.session;
        return switch (session.status) {
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
        };
      },
    ),
  );
}
