import 'dart:async';
import 'dart:convert';
import 'dart:math';
import 'dart:typed_data';
import 'dart:ui' as ui;

import 'package:flutter/material.dart';
import 'package:maplibre_gl/maplibre_gl.dart';

import '../api/api_client.dart';
import '../api/models.dart';
import '../auth/oidc.dart';
import '../data/field_store.dart';
import '../map/map_auth.dart';
import '../map/map_style.dart';
import '../theme/app_theme.dart';
import 'home.dart';
import 'widgets.dart';

/// The user's area on a map, with detections from the permission-filtered
/// tiles. Tap a point to open its alert.
class AlertMapView extends StatefulWidget {
  const AlertMapView({super.key, required this.view, required this.onOpen, this.focus});
  final WorkspaceView view;
  final void Function(Alert alert) onOpen;
  final Alert? focus;

  @override
  State<AlertMapView> createState() => _AlertMapViewState();
}

class _AlertMapViewState extends State<AlertMapView> {
  MapLibreMapController? _controller;
  Timer? _tokenTimer;
  late final Future<void> _ready = _setToken();
  String? _problem;
  Brightness? _imageBrightness;

  String get _slug => widget.view.workspace.slug;

  @override
  void initState() {
    super.initState();
    // Access tokens last minutes. Keep the map's copy fresh.
    _tokenTimer = Timer.periodic(const Duration(minutes: 1), (_) => _setToken());
  }

  @override
  void dispose() {
    _tokenTimer?.cancel();
    unawaited(setMapToken(null));
    super.dispose();
  }

  Future<void> _setToken() async {
    try {
      await setMapToken(await widget.view.services.session.accessToken());
    } on SignInException {
      await setMapToken(null);
    }
  }

  Bounds? get _bounds => jurisdictionBounds(widget.view.grants, widget.view.map);

  Future<void> _styleLoaded() async {
    final controller = _controller;
    if (controller == null || !mounted) return;
    final brightness = Theme.of(context).brightness;
    final ratio = MediaQuery.devicePixelRatioOf(context);
    final colour = FimraColours.of(context).neutralMark;
    try {
      if (_imageBrightness != brightness) {
        await controller.addImage(squareImage, await _squarePng(colour, ratio), false, ratio);
        _imageBrightness = brightness;
      }
    } on Exception {
      // The squares are missing, but circles still show.
    }
    if (widget.focus == null) await _fit();
  }

  Future<void> _fit() async {
    final bounds = _bounds;
    if (bounds == null) return;
    await _controller?.moveCamera(
      CameraUpdate.newLatLngBounds(
        LatLngBounds(southwest: LatLng(bounds.south, bounds.west), northeast: LatLng(bounds.north, bounds.east)),
        left: 32,
        top: 32,
        right: 32,
        bottom: 32,
      ),
    );
  }

  Future<void> _tap(Point<double> point, LatLng _) async {
    final controller = _controller;
    if (controller == null) return;
    final features = await controller.queryRenderedFeatures(point, detectionLayers, null);
    if (features.isEmpty || !mounted) return;
    // The first feature is the one drawn on top.
    final feature = features.first;
    final props = feature is Map ? feature['properties'] : null;
    final id = props is Map ? props['id'] : null;
    if (id == null) return;
    try {
      final alert = await widget.view.services.repository.alertForDetection(_slug, '$id');
      if (!mounted) return;
      if (alert == null) {
        ScaffoldMessenger.of(
          context,
        ).showSnackBar(const SnackBar(content: Text('This detection has no alert for you. It may be outside forest.')));
      } else {
        widget.onOpen(alert);
      }
    } on ApiException catch (error) {
      if (mounted) setState(() => _problem = error.message);
    }
  }

  @override
  Widget build(BuildContext context) {
    final repo = widget.view.services.offline;
    if (repo == null) return _buildMap(context);
    return ValueListenableBuilder(
      valueListenable: repo.changes,
      builder: (context, _, _) => FutureBuilder(
        future: repo.offline || widget.focus != null
            ? (repo.savedReference(_slug), repo.savedAlerts(_slug)).wait
            : null,
        builder: (context, snap) => _buildMap(
          context,
          reference: snap.data?.$1 ?? (repo.offline || widget.focus != null ? const {} : null),
          alerts: widget.focus == null ? snap.data?.$2 ?? [] : [widget.focus!],
        ),
      ),
    );
  }

  Widget _buildMap(BuildContext context, {Json? reference, List<Alert> alerts = const []}) {
    final p = FimraColours.of(context);
    final style = mapStyle(
      referenceTiles: mapTileUrl(widget.view.services.repository.referenceTiles(_slug)),
      detectionTiles: mapTileUrl(widget.view.services.repository.detectionTiles(_slug)),
      info: widget.view.map,
      palette: p,
      jurisdiction: widget.view.grants,
      offlineReference: reference,
      offlineAlerts: alerts,
    );
    final bounds = _bounds;
    final focus = widget.focus;
    final centre = focus != null
        ? LatLng(focus.latitude, focus.longitude)
        : bounds == null
        ? const LatLng(22, 79)
        : LatLng((bounds.south + bounds.north) / 2, (bounds.west + bounds.east) / 2);
    return FutureBuilder<void>(
      future: reference == null ? _ready : null,
      builder: (context, snapshot) {
        if (reference == null && snapshot.connectionState != ConnectionState.done) {
          return const Center(child: CircularProgressIndicator());
        }
        return Stack(
          children: [
            Semantics(
              label: 'Map of your area with fire detections',
              child: MapLibreMap(
                styleString: jsonEncode(style),
                initialCameraPosition: CameraPosition(
                  target: centre,
                  zoom: focus != null
                      ? 13
                      : bounds == null
                      ? 4
                      : 10,
                ),
                onMapCreated: (controller) => _controller = controller,
                onStyleLoadedCallback: () {
                  _imageBrightness = null;
                  unawaited(_styleLoaded());
                },
                onMapClick: focus == null ? _tap : null,
                // Rotation is off, so the map needs no compass.
                compassEnabled: false,
                rotateGesturesEnabled: false,
                tiltGesturesEnabled: false,
                myLocationEnabled: false,
                attributionEnabled: false,
              ),
            ),
            Positioned(
              right: 12,
              top: 12,
              child: Column(
                children: [
                  _MapButton(icon: Icons.center_focus_strong, tooltip: 'Show my area', onPressed: _fit),
                  const SizedBox(height: 8),
                  _MapButton(icon: Icons.info_outline, tooltip: 'Map key', onPressed: () => _showKey(context)),
                ],
              ),
            ),
            if (_problem != null)
              Positioned(
                left: 12,
                right: 12,
                bottom: 12,
                child: Card(
                  child: ListTile(
                    leading: Icon(Icons.error_outline, color: p.danger),
                    title: Text(_problem!),
                    trailing: IconButton(
                      tooltip: 'Dismiss',
                      icon: const Icon(Icons.close),
                      onPressed: () => setState(() => _problem = null),
                    ),
                  ),
                ),
              ),
          ],
        );
      },
    );
  }

  void _showKey(BuildContext context) => showModalBottomSheet<void>(
    context: context,
    showDragHandle: true,
    builder: (context) => const SafeArea(child: MapKey()),
  );
}

class _MapButton extends StatelessWidget {
  const _MapButton({required this.icon, required this.tooltip, required this.onPressed});
  final IconData icon;
  final String tooltip;
  final VoidCallback onPressed;

  @override
  Widget build(BuildContext context) => Material(
    color: FimraColours.of(context).card,
    elevation: 2,
    shape: const CircleBorder(),
    child: IconButton(tooltip: tooltip, icon: Icon(icon), onPressed: onPressed),
  );
}

/// Explains the map symbols: shape for forest class, colour for age.
class MapKey extends StatelessWidget {
  const MapKey({super.key});

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return ListView(
      shrinkWrap: true,
      padding: const EdgeInsets.fromLTRB(16, 0, 16, 16),
      children: [
        Text('Map key', style: text.titleLarge),
        const SizedBox(height: 12),
        Text('Shape shows the forest class', style: text.titleSmall),
        for (final forest in ForestClass.values)
          ListTile(
            contentPadding: EdgeInsets.zero,
            leading: DetectionSymbol(forestClass: forest, size: 20),
            title: Text(forest.label),
          ),
        const SizedBox(height: 8),
        Text('Colour shows time since detection', style: text.titleSmall),
        for (var age = 0; age < fireAgeSteps.length; age++)
          ListTile(
            contentPadding: EdgeInsets.zero,
            leading: DetectionSymbol(forestClass: ForestClass.inside, age: age, size: 20),
            title: Text(fireAgeSteps[age][0].toUpperCase() + fireAgeSteps[age].substring(1)),
          ),
        const SizedBox(height: 8),
        Text('Your own area has a thick outline.', style: text.bodyMedium),
      ],
    );
  }
}

Future<Uint8List> _squarePng(Color colour, double ratio) async {
  final size = (12 * ratio).ceil();
  final recorder = ui.PictureRecorder();
  final canvas = Canvas(recorder);
  final inset = size * 0.1;
  canvas.drawRect(Rect.fromLTWH(inset, inset, size - 2 * inset, size - 2 * inset), Paint()..color = colour);
  final image = await recorder.endRecording().toImage(size, size);
  final data = await image.toByteData(format: ui.ImageByteFormat.png);
  return data!.buffer.asUint8List();
}
