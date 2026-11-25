import 'package:flutter/material.dart';
import 'package:intl/intl.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import '../device/device_manager.dart';
import 'widgets.dart';

/// Whether this phone gets alert notifications, and the user's phones.
class DevicesScreen extends StatefulWidget {
  const DevicesScreen({super.key, required this.devices});
  final DeviceManager devices;

  @override
  State<DevicesScreen> createState() => _DevicesScreenState();
}

class _DevicesScreenState extends State<DevicesScreen> {
  List<Device>? _items;
  String? _problem;
  bool _checking = false;

  @override
  void initState() {
    super.initState();
    _load();
  }

  Future<void> _load() async {
    setState(() => _problem = null);
    try {
      final items = await widget.devices.devices();
      if (mounted) setState(() => _items = items);
    } on DeviceActionException {
      return;
    } on ApiException catch (error) {
      if (mounted) setState(() => _problem = error.message);
    }
  }

  Future<void> _check() async {
    setState(() => _checking = true);
    await widget.devices.retryPush();
    if (!mounted) return;
    setState(() => _checking = false);
    await _load();
  }

  Future<void> _remove(Device device) async {
    final confirmed = await showDialog<bool>(
      context: context,
      builder: (context) => AlertDialog(
        title: Text('Remove this ${device.platformLabel.toLowerCase()}?'),
        content: const Text(
          'It stops getting notifications at once and is signed out the next time it connects. '
          'To delete its data as well, ask your administrator to wipe it.',
        ),
        actions: [
          TextButton(onPressed: () => Navigator.of(context).pop(false), child: const Text('Cancel')),
          FilledButton(onPressed: () => Navigator.of(context).pop(true), child: const Text('Remove')),
        ],
      ),
    );
    if (confirmed != true || !mounted) return;
    final messenger = ScaffoldMessenger.of(context);
    try {
      await widget.devices.remove(device);
      messenger.showSnackBar(const SnackBar(content: Text('The phone was removed.')));
      await _load();
    } on DeviceActionException {
      return;
    } on ApiException catch (error) {
      messenger.showSnackBar(SnackBar(content: Text(error.message)));
    }
  }

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Scaffold(
      appBar: AppBar(title: const Text('Notifications and phones')),
      body: RefreshIndicator(
        onRefresh: _load,
        child: ListView(
          padding: const EdgeInsets.all(16),
          children: [
            ListenableBuilder(
              listenable: widget.devices,
              builder: (context, _) => _ThisPhone(devices: widget.devices, checking: _checking, onCheck: _check),
            ),
            const SizedBox(height: 24),
            Text('Your phones', style: text.titleMedium),
            const SizedBox(height: 4),
            Text('Each sign-in on a phone is listed. Remove a phone you no longer use.', style: text.bodyMedium),
            const SizedBox(height: 12),
            ..._list(),
          ],
        ),
      ),
    );
  }

  List<Widget> _list() {
    final problem = _problem;
    if (problem != null) {
      return [
        MessageView(
          icon: Icons.cloud_off_outlined,
          title: 'Could not load your phones',
          body: problem,
          action: OutlinedButton(onPressed: _load, child: const Text('Try again')),
        ),
      ];
    }
    final items = _items;
    if (items == null) return const [Center(child: CircularProgressIndicator())];
    if (items.isEmpty) return const [Text('No phones are registered yet.')];
    return [for (final device in items) _DeviceCard(device: device, onRemove: () => _remove(device))];
  }
}

class _ThisPhone extends StatelessWidget {
  const _ThisPhone({required this.devices, required this.checking, required this.onCheck});
  final DeviceManager devices;
  final bool checking;
  final VoidCallback onCheck;

  @override
  Widget build(BuildContext context) {
    final status = devices.pushStatus;
    final body = switch (status) {
      PushStatus.unsupported => 'This build of the app cannot receive notifications. Use the Android app.',
      PushStatus.noPushApp =>
        "Install a UnifiedPush app such as ntfy and connect it to your department's push server. "
            'Then choose Check again.',
      PushStatus.waiting => 'Waiting for your push app to give this phone an address.',
      PushStatus.on =>
        'New fire alerts for your area appear on this phone. A notification shows no details '
            'until you open it in the app.',
      PushStatus.refused => devices.pushProblem ?? "The server does not accept this phone's push app.",
      PushStatus.offline => devices.pushProblem ?? 'Check your connection, then choose Check again.',
    };
    final on = status == PushStatus.on;
    return Card(
      child: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.start,
          children: [
            Row(
              children: [
                Icon(on ? Icons.notifications_active_outlined : Icons.notifications_off_outlined),
                const SizedBox(width: 12),
                Expanded(child: Text(status.title, style: Theme.of(context).textTheme.titleMedium)),
              ],
            ),
            const SizedBox(height: 8),
            Text(body),
            if (!on && status != PushStatus.unsupported) ...[
              const SizedBox(height: 12),
              OutlinedButton(onPressed: checking ? null : onCheck, child: const Text('Check again')),
            ],
          ],
        ),
      ),
    );
  }
}

class _DeviceCard extends StatelessWidget {
  const _DeviceCard({required this.device, required this.onRemove});
  final Device device;
  final VoidCallback onRemove;

  static IconData _icon(DeviceStatus status) => switch (status) {
    DeviceStatus.active => Icons.check_circle_outline,
    DeviceStatus.signedOut => Icons.logout,
    DeviceStatus.removed => Icons.remove_circle_outline,
    DeviceStatus.wipePending => Icons.hourglass_top,
    DeviceStatus.wiped => Icons.delete_outline,
  };

  @override
  Widget build(BuildContext context) {
    final seen = DateFormat('d MMM y, HH:mm').format(device.lastSeenAt.toLocal());
    final title = device.current ? '${device.platformLabel} (this phone)' : device.platformLabel;
    return Card(
      child: Padding(
        padding: const EdgeInsets.fromLTRB(16, 12, 8, 12),
        child: Row(
          children: [
            Icon(device.platform == 'web' ? Icons.web_outlined : Icons.phone_android),
            const SizedBox(width: 12),
            Expanded(
              child: Column(
                crossAxisAlignment: CrossAxisAlignment.start,
                children: [
                  Text(title, style: Theme.of(context).textTheme.titleSmall),
                  const SizedBox(height: 2),
                  Text('App ${device.appVersion} · last contact $seen'),
                  const SizedBox(height: 6),
                  Wrap(
                    spacing: 8,
                    runSpacing: 4,
                    children: [
                      StatusBadge(icon: _icon(device.status), label: device.status.label),
                      if (device.status == DeviceStatus.active)
                        StatusBadge(
                          icon: device.push ? Icons.notifications_active_outlined : Icons.notifications_off_outlined,
                          label: device.push ? 'Notifications on' : 'No notifications',
                        ),
                    ],
                  ),
                ],
              ),
            ),
            if (device.status == DeviceStatus.active && !device.current)
              TextButton(
                onPressed: onRemove,
                child: Text('Remove', semanticsLabel: 'Remove ${device.platformLabel}'),
              ),
          ],
        ),
      ),
    );
  }
}
