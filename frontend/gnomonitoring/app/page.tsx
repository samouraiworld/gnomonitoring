"use client";

import { useState } from "react";

export default function Home() {
  const [activeTab, setActiveTab] = useState<"all" | "monthly" | "weekly">("all");

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
      <div className="grid grid-cols-1 md:grid-cols-2 gap-6">
        {/* Last block */}
        <div className="bg-white dark:bg-neutral-800 p-4 rounded-xl shadow">
          <h2 className="text-xl font-semibold mb-2">Block Height</h2>
          <div className="text-sm text-gray-500 dark:text-gray-300">
            Height : <span className="font-medium text-black dark:text-white">#123456</span><br />
          </div>
        </div>

        {/* Latest incidents */}
        <div className="bg-white dark:bg-neutral-800 p-4 rounded-xl shadow">
          <h2 className="text-xl font-semibold mb-2">Latest incidents</h2>
          <ul className="text-sm text-gray-500 dark:text-gray-300 space-y-1">
            <li>[2025-08-07 14:59] ⚠️ Validator XYZ - Missed 3 blocks</li>
            <li>[2025-08-07 14:45] 🔴 Validator ABC - Inactive</li>
            <li>[2025-08-07 13:30] ⚠️ Validator LMN - Low participation</li>
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
          {activeTab === "all" && (
            <div>
              <p className="mb-2 font-semibold">Overall participation:</p>
              <ul className="list-disc pl-5">
                <li>Validator A : 99.5%</li>
                <li>Validator B : 98.7%</li>
                <li>Validator C : 97.2%</li>
              </ul>
            </div>
          )}
          {activeTab === "monthly" && (
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
          )}
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
              href="https://test7.testnets.gno.land/"
              target="_blank"
              className="block px-4 py-2 bg-green-100 dark:bg-green-900 text-green-800 dark:text-green-300 rounded hover:bg-green-200 dark:hover:bg-green-800 transition"
            >
              🌐 GnoWeb Interface
            </a>
          </li>
        </ul>
      </div>
    </section>
  );
}
