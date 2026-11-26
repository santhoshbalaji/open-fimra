import 'package:flutter/material.dart';
import 'package:flutter_svg/flutter_svg.dart';
import 'package:intl/intl.dart';

import '../api/models.dart';
import '../theme/app_theme.dart';

/// Workflow steps that end an alert without closing it normally.
const otherOutcomes = {'false_alarm', 'duplicate', 'controlled_burn', 'not_reachable'};

IconData stepIcon(String step) => switch (step) {
  'new' => Icons.notifications_active_outlined,
  'closed' || 'verified' => Icons.check_circle_outline,
  'extinguished' || 'controlled' => Icons.local_fire_department_outlined,
  'first_report' || 'final_report' => Icons.description_outlined,
  _ when otherOutcomes.contains(step) => Icons.do_not_disturb_on_outlined,
  _ => Icons.schedule,
};

/// A status shown as an icon and a label, never colour alone.
class StatusBadge extends StatelessWidget {
  const StatusBadge({super.key, required this.icon, required this.label, this.iconColour});

  factory StatusBadge.step(String step, String label) => StatusBadge(icon: stepIcon(step), label: label);

  factory StatusBadge.overdue() =>
      const StatusBadge(icon: Icons.warning_amber_rounded, label: 'Overdue', iconColour: FimraStatus.warning);

  final IconData icon;
  final String label;
  final Color? iconColour;

  @override
  Widget build(BuildContext context) {
    final p = FimraColours.of(context);
    assert(label.trim().isNotEmpty, 'A status needs a visible label.');
    return Container(
      padding: const EdgeInsets.symmetric(horizontal: 8, vertical: 3),
      decoration: BoxDecoration(
        color: p.card,
        border: Border.all(color: p.border),
        borderRadius: BorderRadius.circular(FimraSize.radiusPill),
      ),
      child: Row(
        mainAxisSize: MainAxisSize.min,
        children: [
          Icon(icon, size: 16, color: iconColour ?? p.text),
          const SizedBox(width: 6),
          Flexible(child: Text(label, style: Theme.of(context).textTheme.labelMedium)),
        ],
      ),
    );
  }
}

/// The map symbol for a detection: shape shows forest class, colour shows age.
class DetectionSymbol extends StatelessWidget {
  const DetectionSymbol({super.key, required this.forestClass, this.age = 0, this.size = 16});
  final ForestClass? forestClass;
  final int age;
  final double size;

  @override
  Widget build(BuildContext context) {
    final p = FimraColours.of(context);
    return SizedBox.square(
      dimension: size,
      child: CustomPaint(
        painter: _SymbolPainter(forestClass, p.fireAge[age.clamp(0, 3)], p.neutralMark, p.mapAgeOutline),
      ),
    );
  }
}

class _SymbolPainter extends CustomPainter {
  _SymbolPainter(this.forest, this.colour, this.neutral, this.halo);
  final ForestClass? forest;
  final Color colour, neutral, halo;

  @override
  void paint(Canvas canvas, Size size) {
    final centre = size.center(Offset.zero);
    final r = size.shortestSide / 2;
    final fill = Paint()..style = PaintingStyle.fill;
    final stroke = Paint()
      ..style = PaintingStyle.stroke
      ..strokeWidth = r * 0.36;
    switch (forest) {
      case ForestClass.inside:
        canvas.drawCircle(centre, r * 0.86, fill..color = halo);
        canvas.drawCircle(centre, r * 0.70, fill..color = colour);
      case ForestClass.nearEdge:
        canvas.drawCircle(centre, r * 0.64, stroke..color = halo);
        canvas.drawCircle(
          centre,
          r * 0.64,
          stroke
            ..strokeWidth = r * 0.22
            ..color = colour,
        );
      case ForestClass.fixedHeatSource:
        canvas.drawRect(Rect.fromCircle(center: centre, radius: r * 0.65), fill..color = neutral);
      case ForestClass.outside || null:
        canvas.drawCircle(centre, r * 0.4, fill..color = neutral);
    }
  }

  @override
  bool shouldRepaint(_SymbolPainter old) =>
      old.forest != forest || old.colour != colour || old.neutral != neutral || old.halo != halo;
}

/// The project logo in the colours for the current theme.
class FimraLogo extends StatelessWidget {
  const FimraLogo({super.key, this.lockup = false, this.height = 48});
  final bool lockup;
  final double height;

  @override
  Widget build(BuildContext context) {
    final dark = Theme.of(context).brightness == Brightness.dark;
    final name = '${lockup ? 'lockup' : 'mark'}-${dark ? 'dark' : 'light'}.svg';
    return SvgPicture.asset('assets/logo/$name', height: height, semanticsLabel: 'Open FIMRA');
  }
}

/// A centred message with an icon, for empty and error states.
class MessageView extends StatelessWidget {
  const MessageView({super.key, required this.icon, required this.title, this.body, this.action});
  final IconData icon;
  final String title;
  final String? body;
  final Widget? action;

  @override
  Widget build(BuildContext context) {
    final text = Theme.of(context).textTheme;
    return Center(
      child: SingleChildScrollView(
        padding: const EdgeInsets.all(24),
        child: Column(
          mainAxisSize: MainAxisSize.min,
          children: [
            Icon(icon, size: 40, color: FimraColours.of(context).textSecondary),
            const SizedBox(height: 12),
            Text(title, style: text.titleMedium, textAlign: TextAlign.center),
            if (body != null) ...[
              const SizedBox(height: 8),
              Text(body!, style: text.bodyMedium, textAlign: TextAlign.center),
            ],
            if (action != null) ...[const SizedBox(height: 16), action!],
          ],
        ),
      ),
    );
  }
}

/// Times in the phone's own time zone, such as "25 Nov, 15:30".
String formatTime(DateTime time) => DateFormat('d MMM, HH:mm').format(time.toLocal());

/// A short age such as "2 hours ago".
String formatAge(DateTime time, DateTime now) {
  final age = now.difference(time);
  if (age.inMinutes < 1) return 'just now';
  if (age.inMinutes < 60) return _plural(age.inMinutes, 'minute');
  if (age.inHours < 48) return _plural(age.inHours, 'hour');
  return _plural(age.inDays, 'day');
}

String _plural(int n, String unit) => '$n $unit${n == 1 ? '' : 's'} ago';

/// The fire-age step for a detection, as the map tiles count it.
int fireAgeIndex(DateTime observed, DateTime now) {
  final hours = now.difference(observed).inMinutes / 60;
  if (hours < 6) return 0;
  if (hours < 24) return 1;
  if (hours < 72) return 2;
  return 3;
}
