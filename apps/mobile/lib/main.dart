import 'package:flutter/foundation.dart';
import 'package:flutter/material.dart';
import 'package:flutter/semantics.dart';

import 'src/app.dart';
import 'src/config.dart';
import 'src/device/push_platform.dart';
import 'src/map/map_auth.dart';

Future<void> main(List<String> args) async {
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
  if (isBackgroundStart(args)) {
    // The push app started the field app without a screen to hand it a
    // message. Show the notification, or call the API for a device check,
    // without opening the app.
    await AppServices.connect(config).start(foreground: false);
    return;
  }
  runApp(FimraApp(configStore: store, initialConfig: config, servicesFactory: AppServices.connect));
}
