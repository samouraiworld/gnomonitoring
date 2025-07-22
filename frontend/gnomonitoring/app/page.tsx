// app/page.tsx

export default function Home() {
  return (
    <section className="text-center space-y-6">
      <h1 className="text-4xl font-bold">Bienvenue sur GnoMonitoring</h1>
      <p className="text-lg text-gray-600">
        Surveillez vos validateurs et la gouvernance Gno facilement.
      </p>
      <a href="/signup" className="bg-blue-600 text-white px-4 py-2 rounded-xl shadow">Commencer</a>
    </section>
  );
}