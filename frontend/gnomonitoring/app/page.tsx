"use client";
import { Dash } from "@/app/dash";
import { useState } from "react";

export default function Home() {
  const [activeTab, setActiveTab] = useState<"all" | "monthly" | "weekly">("all");

  const {
    blockHeight,
    incidents,
    loading,
    error,
    formatSentAt,
    participationData,
    reload: fetchBlockHeight

  } = Dash(activeTab)

  return (
    <section className="p-4 space-y-8">
      {/* Header */}
      <div className="text-center space-y-2">
        <h1 className="text-4xl font-bold">Welcome to GnoMonitoring</h1>
        <p className="text-lg text-gray-600">
          Monitor your validators and Gno governance easily.
        </p>
      </div>

      {/* Grid: Last Block + Latest Incidents */}
      <div className="grid grid-cols-1 md:grid-cols-5 gap-6">
        {/* Block Height - prend 2/5 de la largeur */}
        <div className="md:col-span-1 bg-white dark:bg-neutral-800 p-4 rounded-xl shadow">
          <h2 className="text-xl font-semibold mb-2 text-center">Block Height :</h2>
          <div className="text-sm text-gray-500 dark:text-gray-300 text-center">
            <span className="text-2xl font-medium text-black dark:text-white text-center">
              {blockHeight !== null ? blockHeight : "loading..."}
            </span><br />
          </div>
        </div>

        {/* Latest incidents - prend 3/5 de la largeur */}
        <div className="md:col-span-4 bg-white dark:bg-neutral-800 p-4 rounded-xl shadow">
          <h2 className="text-xl font-semibold mb-4">Latest incidents</h2>
          <ul className="space-y-3 text-sm text-gray-700 dark:text-gray-300">
            {incidents.length === 0 && <li>No incidents found.</li>}
            {incidents.map((incident, index) => {
              // Choix icône & couleur selon level
              let icon, color;
              switch (incident.Level) {
                case "CRITICAL":
                  icon = "🔴";
                  color = "text-red-600 dark:text-red-400";
                  break;
                case "WARNING":
                  icon = "⚠️";
                  color = "text-yellow-600 dark:text-yellow-400";
                  break;
                case "RESOLVED":
                  icon = "✅";
                  color = "text-green-600 dark:text-green-400";
                  break;
                default:
                  icon = "ℹ️";
                  color = "text-gray-600 dark:text-gray-400";
              }

              return (
                <li key={index} className={`flex items-start space-x-2`}>
                  <span className={`flex-shrink-0 text-lg leading-none `}>

                    {/* <span className={`flex-shrink-0 text-lg leading-none ${color}`}>
                    {icon} */}
                  </span>
                  <div>
                    <span className="font-mono text-xs text-gray-400 dark:text-gray-500">
                      [{formatSentAt(incident.SentAt)}]
                    </span>{" "}
                    <span className={`font-semibold`}>
                      {icon} {incident.Level} {incident.Moniker}
                    </span>{" "}
                    <span className={`text`}>
                      addr : {incident.Addr} missed {incident.EndHeight - incident.StartHeight}
                    </span>{" "}


                  </div>
                </li>
              );
            })}
          </ul>
        </div>
      </div>

      {/* Participation report */}
      <div className="bg-white dark:bg-neutral-800 p-4 rounded-xl shadow">
        <h2 className="text-xl font-semibold mb-4">Participation report</h2>

        {/* Tabs */}
        <div className="flex border-b border-gray-300 dark:border-gray-600 mb-4">
          <button
            onClick={() => setActiveTab("all")}
            className={`px-4 py-2 text-sm font-medium ${activeTab === "all"
              ? "border-b-2 border-blue-600 text-blue-600"
              : "text-gray-500"
              }`}
          >
            All time
          </button>
          <button
            onClick={() => setActiveTab("monthly")}
            className={`px-4 py-2 text-sm font-medium ${activeTab === "monthly"
              ? "border-b-2 border-blue-600 text-blue-600"
              : "text-gray-500"
              }`}
          >
            Monthly
          </button>
          <button
            onClick={() => setActiveTab("weekly")}
            className={`px-4 py-2 text-sm font-medium ${activeTab === "weekly"
              ? "border-b-2 border-blue-600 text-blue-600"
              : "text-gray-500"
              }`}
          >
            Weekly
          </button>
        </div>

        {/* Tab content */}
        <div className="text-sm text-gray-700 dark:text-gray-300">
          {loading && <p>Loading...</p>}
          {!loading && participationData[activeTab] && (
            <div>
              <p className="mb-2 font-semibold">
                {activeTab === "all"
                  ? "Overall participation:"
                  : activeTab === "monthly"
                    ? "Participation this Month:"
                    : "Participation this Week:"}
              </p>
              <ul className="list-disc pl-5">
                {participationData[activeTab]!.map((val) => {
                  let emoji = "🟢";
                  if (val.ParticipationRate < 95.0) emoji = "🟡";
                  if (val.ParticipationRate < 70.0) emoji = "🟠";
                  if (val.ParticipationRate < 50.0) emoji = "🔴";

                  return (
                    <li key={val.Addr}>
                      {emoji} {val.Moniker} {val.Addr} : {val.ParticipationRate.toFixed(1)}%
                    </li>
                  );
                })}
              </ul>
            </div>
          )}
          {/* {activeTab === "monthly" && (
            <div>
              <p className="mb-2 font-semibold">Participation this Month:</p>
              <ul className="list-disc pl-5">
                <li>Validator A : 96.4%</li>
                <li>Validator B : 95.1%</li>
                <li>Validator C : 93.3%</li>
              </ul>
            </div>
          )}
          {activeTab === "weekly" && (
            <div>
              <p className="mb-2 font-semibold">Participation this Week:</p>
              <ul className="list-disc pl-5">
                <li>Validator A : 94.5%</li>
                <li>Validator B : 92.7%</li>
                <li>Validator C : 91.0%</li>
              </ul>
            </div>
          )} */}
        </div>
      </div>

      {/* Useful Links */}
      <div className="bg-white dark:bg-neutral-900 rounded-2xl shadow-md p-6 mt-6">
        <h2 className="text-xl font-semibold mb-4 text-gray-800 dark:text-white">Useful Links</h2>
        <ul className="space-y-3">
          <li>
            <a
              href="https://gnoscan.io"
              target="_blank"
              className="block px-4 py-2 bg-blue-100 dark:bg-blue-900 text-blue-800 dark:text-blue-300 rounded hover:bg-blue-200 dark:hover:bg-blue-800 transition"
            >
              🔍 GnoScan Explorer
            </a>
          </li>
          <li>
            <a
              href="https://gnoweb.test9.testnets.gno.land"
              target="_blank"
              className="block px-4 py-2 bg-green-100 dark:bg-green-900 text-green-800 dark:text-green-300 rounded hover:bg-green-200 dark:hover:bg-green-800 transition"
            >
              🌐 GnoWeb Interface
            </a>
          </li>
          <li>
            <a
              href="https://gnolove.world/"
              target="_blank"
              className="block px-4 py-2 bg-green-100 dark:bg-blue-900 text-blue-800 dark:text-blue-300 rounded hover:bg-blue-200 dark:hover:bg-blue-800 transition"
            >
              🌐 Gnolove
            </a>
          </li>
        </ul>
      </div>
    </section>
  );
}
