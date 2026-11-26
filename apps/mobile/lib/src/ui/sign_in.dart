import 'package:flutter/material.dart';

import '../auth/session.dart';
import '../config.dart';
import '../l10n/field_language.dart';
import '../theme/app_theme.dart';
import 'widgets.dart';

typedef ConfigChanged = Future<void> Function(AppConfig config, {bool reset});

class SignInScreen extends StatelessWidget {
  const SignInScreen({
    super.key,
    required this.session,
    required this.config,
    required this.defaults,
    required this.onConfigChanged,
    this.onLanguageChanged,
  });

  final Session session;
  final AppConfig config;
  final AppConfig defaults;
  final ConfigChanged onConfigChanged;
  final Future<void> Function(Locale?)? onLanguageChanged;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    final busy = session.status == SessionStatus.signingIn;
    final message = session.message;
    return Scaffold(
      body: SafeArea(
        child: Center(
          child: SingleChildScrollView(
            padding: const EdgeInsets.all(24),
            child: ConstrainedBox(
              constraints: const BoxConstraints(maxWidth: 420),
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.stretch,
                children: [
                  const Center(child: FimraLogo(lockup: true, height: 56)),
                  const SizedBox(height: 32),
                  Text(fieldText(context).fieldApp, style: text.headlineSmall, textAlign: TextAlign.center),
                  const SizedBox(height: 8),
                  Text(
                    fieldText(context).signInWithYourDepartment,
                    style: text.bodyMedium,
                    textAlign: TextAlign.center,
                  ),
                  const SizedBox(height: 24),
                  if (message != null) ...[_Problem(message), const SizedBox(height: 16)],
                  FilledButton.icon(
                    onPressed: busy ? null : session.signIn,
                    icon: busy
                        ? const SizedBox.square(dimension: 18, child: CircularProgressIndicator(strokeWidth: 2))
                        : const Icon(Icons.login),
                    label: Text(busy ? fieldText(context).waitingForSignIn : fieldText(context).signIn),
                  ),
                  const SizedBox(height: 8),
                  TextButton.icon(
                    onPressed: busy
                        ? null
                        : () => Navigator.of(context).push(
                            MaterialPageRoute<void>(
                              builder: (_) =>
                                  ServerSettingsScreen(config: config, defaults: defaults, onSave: onConfigChanged),
                            ),
                          ),
                    icon: const Icon(Icons.dns_outlined),
                    label: Text(fieldText(context).serverSettings),
                  ),
                  if (onLanguageChanged case final change?)
                    TextButton(
                      onPressed: () => chooseFieldLanguage(context, change),
                      child: Text(fieldText(context).language),
                    ),
                  const SizedBox(height: 8),
                  Text(
                    fieldText(
                      context,
                    ).serverName((Uri.tryParse(config.apiBaseUrl)?.authority ?? config.apiBaseUrl).toString()),
                    style: text.bodySmall,
                    textAlign: TextAlign.center,
                  ),
                ],
              ),
            ),
          ),
        ),
      ),
    );
  }
}

class _Problem extends StatelessWidget {
  const _Problem(this.message);
  final String message;

  @override
  Widget build(BuildContext context) {
    final p = FimraColours.of(context);
    // Discovery and token transport errors can include server addresses or response codes.
    final serverProblem =
        message.startsWith('Could not reach the sign-in server') ||
        message.startsWith('The sign-in server answered ') ||
        message.startsWith('The sign-in server did not list its ');
    return Container(
      padding: const EdgeInsets.all(12),
      decoration: BoxDecoration(
        border: Border.all(color: p.danger),
        borderRadius: BorderRadius.circular(FimraSize.radiusCard),
      ),
      child: Column(
        crossAxisAlignment: CrossAxisAlignment.start,
        children: [
          Semantics(
            container: true,
            liveRegion: true,
            child: Row(
              crossAxisAlignment: CrossAxisAlignment.start,
              children: [
                Icon(Icons.error_outline, color: p.danger),
                const SizedBox(width: 8),
                Expanded(
                  child: Text(
                    serverProblem ? fieldText(context).cannotReachTheServerCheck : fieldProblem(context, message),
                  ),
                ),
              ],
            ),
          ),
          if (serverProblem)
            ExpansionTile(
              tilePadding: EdgeInsets.zero,
              title: Text(fieldText(context).technicalDetails),
              expandedCrossAxisAlignment: CrossAxisAlignment.stretch,
              children: [SelectableText(message)],
            ),
        ],
      ),
    );
  }
}

/// Lets a user point the app at a development machine or a test server.
class ServerSettingsScreen extends StatefulWidget {
  const ServerSettingsScreen({super.key, required this.config, required this.defaults, required this.onSave});
  final AppConfig config;
  final AppConfig defaults;
  final ConfigChanged onSave;

  @override
  State<ServerSettingsScreen> createState() => _ServerSettingsScreenState();
}

class _ServerSettingsScreenState extends State<ServerSettingsScreen> {
  final _form = GlobalKey<FormState>();
  late final _api = TextEditingController(text: widget.config.apiBaseUrl);
  late final _issuer = TextEditingController(text: widget.config.issuer);
  late final _client = TextEditingController(text: widget.config.clientId);

  @override
  void dispose() {
    _api.dispose();
    _issuer.dispose();
    _client.dispose();
    super.dispose();
  }

  Future<void> _save({bool reset = false}) async {
    if (!reset && !(_form.currentState?.validate() ?? false)) return;
    final navigator = Navigator.of(context);
    await widget.onSave(
      widget.config.copyWith(apiBaseUrl: _api.text, issuer: _issuer.text, clientId: _client.text.trim()),
      reset: reset,
    );
    navigator.pop();
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: Text(fieldText(context).serverSettings)),
      body: Form(
        key: _form,
        child: ListView(
          padding: const EdgeInsets.all(16),
          children: [
            Text(fieldText(context).useTheseToReachA, style: text.bodyMedium),
            const SizedBox(height: 16),
            TextFormField(
              controller: _api,
              keyboardType: TextInputType.url,
              decoration: InputDecoration(
                labelText: fieldText(context).coreAPIAddress,
                hintText: 'https://fimra.example.org',
              ),
              validator: (value) {
                final problem = AppConfig.checkAddress(value ?? '');
                return problem == null ? null : fieldProblem(context, problem);
              },
            ),
            const SizedBox(height: 16),
            TextFormField(
              controller: _issuer,
              keyboardType: TextInputType.url,
              decoration: InputDecoration(
                labelText: fieldText(context).signInAddress,
                hintText: 'https://id.example.org/realms/open-fimra',
                helperText: fieldText(context).theAddressTheCoreAPI,
              ),
              validator: (value) {
                final problem = AppConfig.checkAddress(value ?? '');
                return problem == null ? null : fieldProblem(context, problem);
              },
            ),
            const SizedBox(height: 16),
            TextFormField(
              controller: _client,
              decoration: InputDecoration(labelText: fieldText(context).appClientID),
              validator: (value) => (value ?? '').trim().isEmpty ? fieldText(context).enterTheClientID : null,
            ),
            const SizedBox(height: 24),
            FilledButton(onPressed: _save, child: Text(fieldText(context).save)),
            const SizedBox(height: 8),
            OutlinedButton(
              onPressed: widget.config == widget.defaults ? null : () => _save(reset: true),
              child: Text(fieldText(context).useTheBuiltInSettings),
            ),
          ],
        ),
      ),
    );
  }
}
