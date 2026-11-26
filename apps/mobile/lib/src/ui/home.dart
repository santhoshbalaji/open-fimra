import 'dart:async';

import 'package:flutter/material.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import '../app.dart';
import 'alert_detail.dart';
import 'alert_list.dart';
import 'alert_map.dart';
import 'devices.dart';
import 'outbox_screen.dart';
import 'widgets.dart';

/// Builds the map tab. Widget tests pass a stand-in.
typedef MapBuilder = Widget Function(BuildContext context, WorkspaceView view, void Function(Alert alert) open);

/// The signed-in user's chosen workspace and what the app knows about it.
class WorkspaceView {
  const WorkspaceView({required this.services, required this.profile, required this.workspace, required this.map});
  final AppServices services;
  final Profile profile;
  final Workspace workspace;
  final WorkspaceMap map;

  List<RoleAssignment> get grants => profile.grantsIn(workspace.slug);
}

class HomeScreen extends StatefulWidget {
  const HomeScreen({super.key, required this.services, this.mapBuilder});
  final AppServices services;
  final MapBuilder? mapBuilder;

  @override
  State<HomeScreen> createState() => _HomeScreenState();
}

class _HomeScreenState extends State<HomeScreen> with WidgetsBindingObserver {
  Profile? _profile;
  WorkspaceView? _view;
  String? _problem;
  bool _loading = true;
  int _tab = 0;
  bool _mapBuilt = false;
  var _listKey = GlobalKey<AlertListViewState>();

  @override
  void initState() {
    super.initState();
    WidgetsBinding.instance.addObserver(this);
    widget.services.devices?.openRequest.addListener(_openRequested);
    _load();
  }

  @override
  void dispose() {
    WidgetsBinding.instance.removeObserver(this);
    widget.services.devices?.openRequest.removeListener(_openRequested);
    super.dispose();
  }

  @override
  void didChangeAppLifecycleState(AppLifecycleState state) {
    if (state == AppLifecycleState.resumed) {
      unawaited(widget.services.offline?.sync());
    }
  }

  /// Opens the alert of a tapped notification. The push message held only
  /// its IDs; the alert itself comes from the API with the user's grants.
  Future<void> _openRequested() async {
    final requests = widget.services.devices?.openRequest;
    final notice = requests?.value;
    if (notice == null || _loading) return;
    requests!.value = null;
    final slug = notice.workspace;
    final alertId = notice.alert;
    if (slug == null || alertId == null) return;
    if (slug != _view?.workspace.slug) {
      if (_profile?.workspaces.any((w) => w.slug == slug) != true) return;
      await _load(slug);
    }
    final view = _view;
    if (!mounted || view == null || view.workspace.slug != slug) return;
    try {
      final alert = await widget.services.repository.alert(slug, alertId);
      if (mounted) await _open(alert);
    } on DeviceActionException {
      return;
    } on ApiException catch (error) {
      if (!mounted) return;
      ScaffoldMessenger.of(context).showSnackBar(SnackBar(content: Text(error.message)));
    }
  }

  Future<void> _load([String? slug]) async {
    setState(() {
      _loading = true;
      _problem = null;
    });
    try {
      final repo = widget.services.repository;
      final profile = await repo.profile();
      final workspaces = profile.workspaces;
      final workspace = workspaces.where((w) => w.slug == slug).firstOrNull ?? workspaces.firstOrNull;
      final map = workspace == null ? null : await repo.workspace(workspace.slug);
      if (!mounted) return;
      setState(() {
        if (workspace?.slug != _view?.workspace.slug) {
          _listKey = GlobalKey<AlertListViewState>();
          _mapBuilt = _tab == 1;
        }
        _profile = profile;
        _view = workspace == null || map == null
            ? null
            : WorkspaceView(services: widget.services, profile: profile, workspace: workspace, map: map);
        _loading = false;
      });
      if (workspace != null) widget.services.offline?.activate(workspace.slug);
      // A notification tapped while the app was starting opens now.
      unawaited(_openRequested());
    } on DeviceActionException {
      // The phone was wiped or removed; the sign-in screen explains.
      return;
    } on ApiException catch (error) {
      if (!mounted) return;
      if (error.status == 401) {
        await widget.services.sessionEnded();
        return;
      }
      setState(() {
        _problem = error.message;
        _loading = false;
      });
    }
  }

  Future<void> _open(Alert alert) async {
    final view = _view;
    if (view == null) return;
    final changed = await Navigator.of(context).push(
      MaterialPageRoute<bool>(
        builder: (_) => AlertDetailScreen(view: view, alert: alert),
      ),
    );
    if (changed == true) await _listKey.currentState?.reload();
  }

  Future<void> _signOut() async {
    final repo = widget.services.offline;
    if (repo != null) {
      final reports = await repo.reportStatus();
      if (!mounted) return;
      if (reports.any((row) => row['received_at'] == null)) {
        final confirmed = await showDialog<bool>(
          context: context,
          builder: (context) => AlertDialog(
            title: const Text('Sign out and clear the outbox?'),
            content: const Text('Signing out deletes reports that are still waiting to send.'),
            actions: [
              TextButton(onPressed: () => Navigator.pop(context, false), child: const Text('Cancel')),
              FilledButton(onPressed: () => Navigator.pop(context, true), child: const Text('Sign out')),
            ],
          ),
        );
        if (!mounted || confirmed != true) return;
      }
    }
    await widget.services.signOut();
  }

  void _openDevices() {
    final devices = widget.services.devices;
    if (devices == null) return;
    Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => DevicesScreen(devices: devices)));
  }

  Future<void> _chooseWorkspace() async {
    final profile = _profile;
    if (profile == null) return;
    final slug = await showModalBottomSheet<String>(
      context: context,
      showDragHandle: true,
      builder: (context) => SafeArea(
        child: ListView(
          shrinkWrap: true,
          children: [
            for (final workspace in profile.workspaces)
              ListTile(
                leading: Icon(
                  workspace.slug == _view?.workspace.slug ? Icons.radio_button_checked : Icons.radio_button_off,
                ),
                title: Text(workspace.name),
                onTap: () => Navigator.of(context).pop(workspace.slug),
              ),
          ],
        ),
      ),
    );
    if (slug != null && slug != _view?.workspace.slug) await _load(slug);
  }

  @override
  Widget build(BuildContext context) {
    final view = _view;
    final profile = _profile;
    final multiple = (profile?.workspaces.length ?? 0) > 1;
    return Scaffold(
      appBar: AppBar(
        title: view == null
            ? const Text('Open FIMRA')
            : InkWell(
                onTap: multiple ? _chooseWorkspace : null,
                child: ConstrainedBox(
                  constraints: const BoxConstraints(minHeight: 48),
                  child: Row(
                    mainAxisSize: MainAxisSize.min,
                    children: [
                      const FimraLogo(height: 28),
                      const SizedBox(width: 10),
                      Flexible(child: Text(view.workspace.name, overflow: TextOverflow.ellipsis)),
                      if (multiple) const Icon(Icons.arrow_drop_down),
                    ],
                  ),
                ),
              ),
        actions: [
          PopupMenuButton<String>(
            tooltip: 'Account',
            icon: const Icon(Icons.account_circle_outlined),
            onSelected: (value) {
              if (value == 'workspace') _chooseWorkspace();
              if (value == 'devices') _openDevices();
              final repo = widget.services.offline;
              if (value == 'outbox' && repo != null) {
                Navigator.of(context).push(MaterialPageRoute<void>(builder: (_) => OutboxScreen(repo: repo)));
              }
              if (value == 'sign-out') unawaited(_signOut());
            },
            itemBuilder: (context) => [
              PopupMenuItem(
                enabled: false,
                child: Text('Signed in as ${profile?.username ?? widget.services.session.username ?? ''}'),
              ),
              if (multiple) const PopupMenuItem(value: 'workspace', child: Text('Change workspace')),
              if (widget.services.devices != null)
                const PopupMenuItem(value: 'devices', child: Text('Notifications and phones')),
              if (widget.services.offline != null) const PopupMenuItem(value: 'outbox', child: Text('Report outbox')),
              const PopupMenuItem(value: 'sign-out', child: Text('Sign out')),
            ],
          ),
        ],
      ),
      body: _body(view),
      bottomNavigationBar: view == null
          ? null
          : NavigationBar(
              selectedIndex: _tab,
              onDestinationSelected: (index) => setState(() {
                _tab = index;
                if (index == 1) _mapBuilt = true;
              }),
              destinations: const [
                NavigationDestination(
                  icon: Icon(Icons.list_alt_outlined),
                  selectedIcon: Icon(Icons.list_alt),
                  label: 'Alerts',
                ),
                NavigationDestination(icon: Icon(Icons.map_outlined), selectedIcon: Icon(Icons.map), label: 'Map'),
              ],
            ),
    );
  }

  Widget _body(WorkspaceView? view) {
    if (_loading) return const Center(child: CircularProgressIndicator());
    final problem = _problem;
    if (problem != null) {
      return MessageView(
        icon: Icons.cloud_off_outlined,
        title: 'Could not load your workspaces',
        body: problem,
        action: FilledButton(onPressed: _load, child: const Text('Try again')),
      );
    }
    if (view == null) {
      return MessageView(
        icon: Icons.lock_outline,
        title: 'You have no access to a workspace yet',
        body: 'Ask your administrator to give you a role, then try again.',
        action: OutlinedButton(onPressed: _load, child: const Text('Try again')),
      );
    }
    return IndexedStack(
      index: _tab,
      children: [
        AlertListView(key: _listKey, view: view, onOpen: _open),
        // The map is built the first time its tab opens, then kept.
        _mapBuilt ? (widget.mapBuilder ?? _defaultMap)(context, view, _open) : const SizedBox.shrink(),
      ],
    );
  }

  static Widget _defaultMap(BuildContext context, WorkspaceView view, void Function(Alert) open) =>
      AlertMapView(key: ValueKey('map-${view.workspace.slug}'), view: view, onOpen: open);
}
