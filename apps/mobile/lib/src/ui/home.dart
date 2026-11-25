import 'package:flutter/material.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import '../app.dart';
import 'alert_detail.dart';
import 'alert_list.dart';
import 'alert_map.dart';
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

class _HomeScreenState extends State<HomeScreen> {
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
    _load();
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
    } on ApiException catch (error) {
      if (!mounted) return;
      if (error.status == 401) {
        await widget.services.session.signOut();
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
              if (value == 'sign-out') widget.services.session.signOut();
            },
            itemBuilder: (context) => [
              PopupMenuItem(
                enabled: false,
                child: Text('Signed in as ${profile?.username ?? widget.services.session.username ?? ''}'),
              ),
              if (multiple) const PopupMenuItem(value: 'workspace', child: Text('Change workspace')),
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
