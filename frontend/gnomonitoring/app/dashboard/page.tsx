import Link from 'next/link';
import { Card } from '../components/Card';
export default function Dashboard() {
    return (
        <div className="grid gap-6 sm:grid-cols-2 md:grid-cols-3">
            <Card title="GovDAO Alerts" link="/dashboard/govdao" />
            <Card title="Validator Monitoring" link="/dashboard/validator" />
            <Card title="Daily Report Settings" link="/dashboard/report" />
        </div>
    );
}