import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/semantics.dart';

import 'src/app.dart';
import 'src/config.dart';
import 'src/map/map_auth.dart';

Future<void> main() async {
  WidgetsFlutterBinding.ensureInitialized();
  // In a browser, keep the accessibility tree on so screen readers and the
  // automated browser tests can find every control by its label.
  if (kIsWeb) SemanticsBinding.instance.ensureSemantics();
  configureMapLibrary();
  var defaults = AppConfig.fromEnvironment();
  if (kIsWeb && defaults.redirectUri.isEmpty) {
    // The browser test build signs in through auth.html on its own address.
    defaults = defaults.copyWith(redirectUri: Uri.base.resolve('auth.html').toString());
  }
  final store = ConfigStore(defaults);
  final config = await store.load();
  runApp(FimraApp(configStore: store, initialConfig: config, servicesFactory: AppServices.connect));
}
